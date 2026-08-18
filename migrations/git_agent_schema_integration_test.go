package migrations

import (
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// The git-agent history tables carry the constraints the ingest watcher relies
// on to be idempotent: it re-scans the same mailbox state repeatedly and upserts
// on natural keys, so those keys have to be enforced by the database rather than
// by the watcher remembering what it already wrote.
var _ = Describe("Captain git-agent schema", func() {
	It("enforces the keys and cascades the ingest watcher depends on", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_git_agent_schema"})
		dsn, db := handle.DSN(), handle.SQL()

		Expect(Apply(ctx, dsn)).To(Succeed())

		insertTask := func(mailbox, taskID string) (string, error) {
			var id string
			err := db.QueryRowContext(ctx, `
				INSERT INTO public.captain_git_agent_tasks
					(task_id, mailbox, base, dispatch_commit)
				VALUES ($1, $2, 'main', 'deadbeef')
				RETURNING id`, taskID, mailbox).Scan(&id)
			return id, err
		}

		taskID, err := insertTask("mailboxes/aaa.git", "task-1")
		Expect(err).NotTo(HaveOccurred())

		By("scoping the task id to its mailbox, because one endpoint routes many repositories")
		_, err = insertTask("mailboxes/aaa.git", "task-1")
		Expect(err).To(MatchError(ContainSubstring("captain_git_agent_tasks_mailbox_task_key")))
		_, err = insertTask("mailboxes/bbb.git", "task-1")
		Expect(err).NotTo(HaveOccurred())

		By("defaulting a fresh task to dispatched with no verdict")
		var status string
		var finalStatus *string
		Expect(db.QueryRowContext(ctx,
			`SELECT status, final_status FROM public.captain_git_agent_tasks WHERE id = $1`, taskID).
			Scan(&status, &finalStatus)).To(Succeed())
		Expect(status).To(Equal("dispatched"))
		Expect(finalStatus).To(BeNil())

		insertAttempt := func(attempt int, tier, verdict string) error {
			_, err := db.ExecContext(ctx, `
				INSERT INTO public.captain_git_agent_task_attempts
					(task_id, attempt, tier, status)
				VALUES ($1, $2, $3, $4)`, taskID, attempt, tier, verdict)
			return err
		}

		By("letting both tiers reach their own verdict on the same attempt")
		Expect(insertAttempt(1, "sidecar", "accepted")).To(Succeed())
		Expect(insertAttempt(1, "supervisor", "rejected")).To(Succeed())

		By("rejecting a duplicate verdict for one tier and attempt")
		Expect(insertAttempt(1, "supervisor", "accepted")).
			To(MatchError(ContainSubstring("captain_git_agent_task_attempts_task_attempt_tier_key")))

		By("refusing a tier the protocol does not define")
		Expect(insertAttempt(2, "supervisorr", "accepted")).
			To(MatchError(ContainSubstring("captain_git_agent_task_attempts_tier")))

		By("refusing a non-positive attempt")
		Expect(insertAttempt(0, "sidecar", "accepted")).
			To(MatchError(ContainSubstring("captain_git_agent_task_attempts_attempt_positive")))

		By("refusing a conclusion that precedes its dispatch")
		_, err = db.ExecContext(ctx, `
			UPDATE public.captain_git_agent_tasks
			SET concluded_at = dispatched_at - interval '1 hour' WHERE id = $1`, taskID)
		Expect(err).To(MatchError(ContainSubstring("captain_git_agent_tasks_time_order")))

		By("cascading attempts when their task is deleted")
		_, err = db.ExecContext(ctx,
			`DELETE FROM public.captain_git_agent_tasks WHERE id = $1`, taskID)
		Expect(err).NotTo(HaveOccurred())
		var remaining int
		Expect(db.QueryRowContext(ctx,
			`SELECT count(*) FROM public.captain_git_agent_task_attempts WHERE task_id = $1`, taskID).
			Scan(&remaining)).To(Succeed())
		Expect(remaining).To(Equal(0))
	})

	// The prompt-run link is filled after the fact — persistPromptRun writes its
	// row only once the run finishes, by which time the remote task has already
	// concluded — so the task must outlive the run row rather than cascade with it.
	It("keeps task history when its prompt run is deleted", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_git_agent_prompt_run_link"})
		dsn, db := handle.DSN(), handle.SQL()

		Expect(Apply(ctx, dsn)).To(Succeed())

		var sessionID string
		Expect(db.QueryRowContext(ctx, `
			INSERT INTO public.captain_sessions (id, source) VALUES (gen_random_uuid(), 'claude')
			RETURNING id`).Scan(&sessionID)).To(Succeed())

		var runID string
		Expect(db.QueryRowContext(ctx, `
			INSERT INTO public.captain_prompt_runs (session_id, root_session_id, admission_key)
			VALUES ($1, $1, 'run-key-1') RETURNING id`, sessionID).Scan(&runID)).To(Succeed())

		var taskID string
		Expect(db.QueryRowContext(ctx, `
			INSERT INTO public.captain_git_agent_tasks
				(task_id, mailbox, base, dispatch_commit, prompt_run_id, admission_key)
			VALUES ('task-1', 'mailboxes/aaa.git', 'main', 'deadbeef', $1, 'run-key-1')
			RETURNING id`, runID).Scan(&taskID)).To(Succeed())

		_, err := db.ExecContext(ctx,
			`DELETE FROM public.captain_prompt_runs WHERE id = $1`, runID)
		Expect(err).NotTo(HaveOccurred())

		var linked *string
		var admissionKey string
		Expect(db.QueryRowContext(ctx,
			`SELECT prompt_run_id, admission_key FROM public.captain_git_agent_tasks WHERE id = $1`, taskID).
			Scan(&linked, &admissionKey)).To(Succeed())
		Expect(linked).To(BeNil(), "the task row must survive its prompt run")
		Expect(admissionKey).To(Equal("run-key-1"), "the correlation handle must survive too")
	})
})
