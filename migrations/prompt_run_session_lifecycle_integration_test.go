package migrations

import (
	"context"
	"database/sql"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const lifecycleScript = "84_prompt_run_session_lifecycle.sql"

// sessionLifecycle is the projected slice of a captain_sessions row.
type sessionLifecycle struct {
	Lifecycle    string
	Activity     string
	Reason       string
	StateVersion int64
	EndedAt      *time.Time
}

var _ = Describe("prompt run session lifecycle", Ordered, func() {
	var (
		dsn      string
		db       *sql.DB
		listener *pgx.Conn
		base     time.Time
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_prompt_run_session_lifecycle"})
		dsn, db = handle.DSN(), handle.SQL()
		Expect(Apply(ctx, dsn)).To(Succeed())
		base = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)
	})

	BeforeEach(func(ctx SpecContext) {
		var err error
		listener, err = pgx.Connect(ctx, dsn)
		Expect(err).NotTo(HaveOccurred())
		_, err = listener.Exec(ctx, "LISTEN "+sessionChangeChannel)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(listener.Close(context.Background())).To(Succeed()) })
	})

	insertSession := func(ctx context.Context, source string) uuid.UUID {
		GinkgoHelper()
		id := uuid.New()
		_, err := db.ExecContext(ctx, `INSERT INTO captain_sessions (id, source) VALUES ($1, $2)`, id, source)
		Expect(err).NotTo(HaveOccurred())
		return id
	}
	// insertRun books a pending run queued at the given offset from base, so
	// specs control which of several runs on one session is the latest.
	insertRun := func(ctx context.Context, admission uuid.UUID, execution *uuid.UUID, queued time.Duration) uuid.UUID {
		GinkgoHelper()
		id := uuid.New()
		at := base.Add(queued)
		_, err := db.ExecContext(ctx, `
			INSERT INTO captain_prompt_runs (id, session_id, root_session_id, execution_session_id, queued_at, created_at)
			VALUES ($1, $2, $2, $3, $4, $4)`, id, admission, execution, at)
		Expect(err).NotTo(HaveOccurred())
		return id
	}
	startRun := func(ctx context.Context, run uuid.UUID, at time.Duration) {
		GinkgoHelper()
		_, err := db.ExecContext(ctx, `
			UPDATE captain_prompt_runs SET state = 'running', phase = 'generate', started_at = $2 WHERE id = $1`,
			run, base.Add(at))
		Expect(err).NotTo(HaveOccurred())
	}
	finishRun := func(ctx context.Context, run uuid.UUID, state, message string, at time.Duration) {
		GinkgoHelper()
		_, err := db.ExecContext(ctx, `
			UPDATE captain_prompt_runs SET state = $2::captain_prompt_run_state, phase = 'finished',
			       error = NULLIF($3, ''), finished_at = $4
			 WHERE id = $1`, run, state, message, base.Add(at))
		Expect(err).NotTo(HaveOccurred())
	}
	lifecycleOf := func(ctx context.Context, id uuid.UUID) sessionLifecycle {
		GinkgoHelper()
		var out sessionLifecycle
		var reason sql.NullString
		var ended sql.NullTime
		Expect(db.QueryRowContext(ctx, `
			SELECT lifecycle_status::text, activity_state::text, state_reason, state_version, ended_at
			  FROM captain_sessions WHERE id = $1`, id).
			Scan(&out.Lifecycle, &out.Activity, &reason, &out.StateVersion, &ended)).To(Succeed())
		out.Reason = reason.String
		if ended.Valid {
			endedAt := ended.Time.UTC()
			out.EndedAt = &endedAt
		}
		return out
	}
	at := func(offset time.Duration) *time.Time {
		value := base.Add(offset)
		return &value
	}

	It("installs the projection trigger as a revoked SECURITY DEFINER function", func(ctx SpecContext) {
		var timing string
		Expect(db.QueryRowContext(ctx, `
			SELECT pg_get_triggerdef(t.oid) FROM pg_trigger t
			 WHERE t.tgname = 'captain_prompt_runs_session_lifecycle_after'`).Scan(&timing)).To(Succeed())
		Expect(timing).To(ContainSubstring("AFTER INSERT OR UPDATE OF state, session_id, execution_session_id ON public.captain_prompt_runs"))

		var securityDefiner, publicExecute bool
		var config string
		Expect(db.QueryRowContext(ctx, `
			SELECT p.prosecdef, has_function_privilege('public', p.oid, 'EXECUTE'), array_to_string(p.proconfig, ',')
			  FROM pg_proc p WHERE p.proname = 'captain_project_prompt_run_lifecycle'`).
			Scan(&securityDefiner, &publicExecute, &config)).To(Succeed())
		Expect(securityDefiner).To(BeTrue())
		Expect(publicExecute).To(BeFalse())
		Expect(config).To(Equal("search_path=pg_catalog, public"))
	})

	It("leaves both sessions alone while the run is pending", func(ctx SpecContext) {
		admission, execution := insertSession(ctx, "gavel"), insertSession(ctx, "claude")
		insertRun(ctx, admission, &execution, 0)

		Expect(lifecycleOf(ctx, admission)).To(Equal(sessionLifecycle{Lifecycle: "created", Activity: "idle"}))
		Expect(lifecycleOf(ctx, execution)).To(Equal(sessionLifecycle{Lifecycle: "created", Activity: "idle"}))
	})

	It("marks the admission and execution sessions running once each", func(ctx SpecContext) {
		admission, execution := insertSession(ctx, "gavel"), insertSession(ctx, "claude")
		run := insertRun(ctx, admission, &execution, 0)
		drainNotifications(ctx, listener)

		startRun(ctx, run, time.Minute)

		running := sessionLifecycle{Lifecycle: "running", Activity: "idle", StateVersion: 1}
		Expect(lifecycleOf(ctx, admission)).To(Equal(running))
		Expect(lifecycleOf(ctx, execution)).To(Equal(running))
		Expect(drainNotifications(ctx, listener)).To(ConsistOf(admission.String(), execution.String()))
	})

	It("finishes both sessions at the run's finished_at with one notification each", func(ctx SpecContext) {
		admission, execution := insertSession(ctx, "gavel"), insertSession(ctx, "claude")
		run := insertRun(ctx, admission, &execution, 0)
		startRun(ctx, run, time.Minute)
		drainNotifications(ctx, listener)

		finishRun(ctx, run, "succeeded", "", 30*time.Minute)

		finished := sessionLifecycle{Lifecycle: "succeeded", Activity: "idle", StateVersion: 2, EndedAt: at(30 * time.Minute)}
		Expect(lifecycleOf(ctx, admission)).To(Equal(finished))
		Expect(lifecycleOf(ctx, execution)).To(Equal(finished))
		Expect(drainNotifications(ctx, listener)).To(ConsistOf(admission.String(), execution.String()))
	})

	DescribeTable("maps a terminal run state onto the session lifecycle",
		func(ctx SpecContext, state, message string, expected sessionLifecycle) {
			admission, execution := insertSession(ctx, "gavel"), insertSession(ctx, "claude")
			run := insertRun(ctx, admission, &execution, 0)
			startRun(ctx, run, time.Minute)

			finishRun(ctx, run, state, message, 5*time.Minute)

			expected.EndedAt = at(5 * time.Minute)
			Expect(lifecycleOf(ctx, admission)).To(Equal(expected))
			Expect(lifecycleOf(ctx, execution)).To(Equal(expected))
		},
		Entry("failed keeps the run error as the reason", "failed", "verify failed: tests red",
			sessionLifecycle{Lifecycle: "failed", Activity: "idle", Reason: "verify failed: tests red", StateVersion: 2}),
		Entry("cancelled stays cancelled", "cancelled", "",
			sessionLifecycle{Lifecycle: "cancelled", Activity: "idle", StateVersion: 2}),
	)

	It("reopens a shared execution session for the next run and ignores the older run's late write", func(ctx SpecContext) {
		execution := insertSession(ctx, "claude")
		firstAdmission, secondAdmission := insertSession(ctx, "gavel"), insertSession(ctx, "gavel")
		first := insertRun(ctx, firstAdmission, &execution, 0)
		startRun(ctx, first, time.Minute)
		finishRun(ctx, first, "failed", "attempt one", 10*time.Minute)
		Expect(lifecycleOf(ctx, execution).Lifecycle).To(Equal("failed"))

		second := insertRun(ctx, secondAdmission, &execution, 20*time.Minute)
		startRun(ctx, second, 21*time.Minute)
		Expect(lifecycleOf(ctx, execution)).To(Equal(sessionLifecycle{Lifecycle: "running", Activity: "idle", StateVersion: 3}))

		_, err := db.ExecContext(ctx, `UPDATE captain_prompt_runs SET state = 'cancelled' WHERE id = $1`, first)
		Expect(err).NotTo(HaveOccurred())
		Expect(lifecycleOf(ctx, execution).Lifecycle).To(Equal("running"))

		finishRun(ctx, second, "succeeded", "", 40*time.Minute)
		Expect(lifecycleOf(ctx, execution)).To(Equal(sessionLifecycle{
			Lifecycle: "succeeded", Activity: "idle", StateVersion: 4, EndedAt: at(40 * time.Minute),
		}))
	})

	// A batch's runs are booked against its member sessions; the root's partial
	// verdict is Go's, derived from all of them.
	It("leaves a batch root's partial verdict and its version untouched by its members' runs", func(ctx SpecContext) {
		root := insertSession(ctx, "captain")
		child := insertSession(ctx, "captain")
		_, err := db.ExecContext(ctx, `UPDATE captain_sessions SET parent_session_id = $1, root_session_id = $1 WHERE id = $2`, root, child)
		Expect(err).NotTo(HaveOccurred())
		_, err = db.ExecContext(ctx, `UPDATE captain_sessions SET lifecycle_status = 'partial' WHERE id = $1`, root)
		Expect(err).NotTo(HaveOccurred())
		before := lifecycleOf(ctx, root)

		childRun := insertRun(ctx, child, nil, 0)
		startRun(ctx, childRun, time.Minute)
		finishRun(ctx, childRun, "succeeded", "", 2*time.Minute)

		Expect(lifecycleOf(ctx, child).Lifecycle).To(Equal("succeeded"))
		Expect(lifecycleOf(ctx, root)).To(Equal(before))
	})

	It("keeps a partial session when a run booked on it goes terminal", func(ctx SpecContext) {
		root := insertSession(ctx, "captain")
		run := insertRun(ctx, root, nil, 0)
		_, err := db.ExecContext(ctx, `UPDATE captain_sessions SET lifecycle_status = 'partial' WHERE id = $1`, root)
		Expect(err).NotTo(HaveOccurred())
		before := lifecycleOf(ctx, root)

		finishRun(ctx, run, "failed", "member failed", 2*time.Minute)

		Expect(lifecycleOf(ctx, root)).To(Equal(before))
	})

	It("backfills sessions whose runs finished before the trigger existed", func(ctx SpecContext) {
		_, err := db.ExecContext(ctx, `ALTER TABLE captain_prompt_runs DISABLE TRIGGER captain_prompt_runs_session_lifecycle_after`)
		Expect(err).NotTo(HaveOccurred())
		reenabled := false
		DeferCleanup(func(ctx context.Context) {
			if !reenabled {
				_, err := db.ExecContext(ctx, `ALTER TABLE captain_prompt_runs ENABLE TRIGGER captain_prompt_runs_session_lifecycle_after`)
				Expect(err).NotTo(HaveOccurred())
			}
		})

		admission, execution := insertSession(ctx, "gavel"), insertSession(ctx, "claude")
		finished := insertRun(ctx, admission, &execution, 0)
		startRun(ctx, finished, time.Minute)
		finishRun(ctx, finished, "succeeded", "", 30*time.Minute)

		// Two attempts share one execution session. The older attempt finished
		// last; the newer one is still the latest run and its outcome wins.
		shared := insertSession(ctx, "claude")
		older := insertRun(ctx, insertSession(ctx, "gavel"), &shared, 0)
		newer := insertRun(ctx, insertSession(ctx, "gavel"), &shared, 5*time.Minute)
		startRun(ctx, older, time.Minute)
		startRun(ctx, newer, 6*time.Minute)
		finishRun(ctx, newer, "failed", "attempt two", 20*time.Minute)
		finishRun(ctx, older, "succeeded", "", 25*time.Minute)

		live := insertSession(ctx, "gavel")
		done := insertRun(ctx, live, nil, 0)
		startRun(ctx, done, time.Minute)
		finishRun(ctx, done, "succeeded", "", 2*time.Minute)
		startRun(ctx, insertRun(ctx, live, nil, 3*time.Minute), 4*time.Minute)

		judged := insertSession(ctx, "captain")
		_, err = db.ExecContext(ctx, `UPDATE captain_sessions SET lifecycle_status = 'partial' WHERE id = $1`, judged)
		Expect(err).NotTo(HaveOccurred())
		judgedRun := insertRun(ctx, judged, nil, 0)
		startRun(ctx, judgedRun, time.Minute)
		finishRun(ctx, judgedRun, "failed", "", 2*time.Minute)
		judgedBefore := lifecycleOf(ctx, judged)

		_, err = db.ExecContext(ctx, `ALTER TABLE captain_prompt_runs ENABLE TRIGGER captain_prompt_runs_session_lifecycle_after`)
		Expect(err).NotTo(HaveOccurred())
		reenabled = true
		script, err := schemaFS.ReadFile(lifecycleScript)
		Expect(err).NotTo(HaveOccurred())
		_, err = db.ExecContext(ctx, string(script))
		Expect(err).NotTo(HaveOccurred())

		succeeded := sessionLifecycle{Lifecycle: "succeeded", Activity: "idle", StateVersion: 1, EndedAt: at(30 * time.Minute)}
		Expect(lifecycleOf(ctx, admission)).To(Equal(succeeded))
		Expect(lifecycleOf(ctx, execution)).To(Equal(succeeded))
		Expect(lifecycleOf(ctx, shared)).To(Equal(sessionLifecycle{
			Lifecycle: "failed", Activity: "idle", Reason: "attempt two", StateVersion: 1, EndedAt: at(20 * time.Minute),
		}))
		Expect(lifecycleOf(ctx, live)).To(Equal(sessionLifecycle{Lifecycle: "created", Activity: "idle"}))
		Expect(lifecycleOf(ctx, judged)).To(Equal(judgedBefore))

		_, err = db.ExecContext(ctx, string(script))
		Expect(err).NotTo(HaveOccurred())
		Expect(lifecycleOf(ctx, admission)).To(Equal(succeeded), "re-running the backfill changes nothing")
	})
})
