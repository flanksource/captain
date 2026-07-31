package migrations

import (
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// 02_merge_duplicate_sessions.sql collapses the session rows the old wide
// identity key allowed, and it does that with a DELETE. Everything reachable from
// a deleted row by an ON DELETE CASCADE is therefore at risk, which is why the
// migration re-points references first. The references that matter most are the
// self-referential hierarchy and the process row: deleting a ghost otherwise
// takes both the subagent transcript and the live process identity with it.
var _ = Describe("Captain duplicate session collapse", func() {
	It("re-points a subagent at the surviving row instead of cascading it away", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_merge_duplicates"})
		dsn, db := handle.DSN(), handle.SQL()

		Expect(Apply(ctx, dsn)).To(Succeed())

		// Recreate the state this migration exists to clean up: the wide key that
		// allowed the duplicates, and a ledger with no record of the collapse. A
		// script is selected by hash, so without clearing the row the collapse is
		// skipped as already-applied -- on a fresh database it "ran" during the Apply
		// above, before captain_sessions existed, and did nothing.
		_, err := db.ExecContext(ctx, `DROP INDEX captain_sessions_provider_identity_key`)
		Expect(err).NotTo(HaveOccurred())
		_, err = db.ExecContext(ctx,
			`DELETE FROM schema_migration_scripts WHERE scope = $1 AND path = $2`,
			Scope, "02_merge_duplicate_sessions.sql")
		Expect(err).NotTo(HaveOccurred())

		const (
			winnerID   = "11111111-1111-1111-1111-111111111111"
			ghostID    = "22222222-2222-2222-2222-222222222222"
			subagentID = "33333333-3333-3333-3333-333333333333"
			processID  = "44444444-4444-4444-4444-444444444444"
		)

		// One rollout, two rows: the monitor's (ingested, provider '') and the
		// launcher's ghost (no messages, provider 'codex-agent'). The subagent
		// records the ghost as its parent, because the launcher wrote both.
		_, err = db.ExecContext(ctx, `
			INSERT INTO captain_sessions (id, source, provider, host_id, provider_session_id, created_at)
			VALUES ($1, 'codex', '',            'host-a', 'rollout-1', now() - interval '2 minutes'),
			       ($2, 'codex', 'codex-agent', 'host-a', 'rollout-1', now() - interval '1 minute')`,
			winnerID, ghostID)
		Expect(err).NotTo(HaveOccurred())

		_, err = db.ExecContext(ctx, `
			INSERT INTO captain_messages (session_id, sequence, role, parts)
			VALUES ($1, 1, 'assistant', '[]'::jsonb)`, winnerID)
		Expect(err).NotTo(HaveOccurred())

		_, err = db.ExecContext(ctx, `
			INSERT INTO captain_sessions (id, source, host_id, provider_session_id, parent_session_id, root_session_id)
			VALUES ($1, 'codex', 'host-a', 'rollout-1-sub', $2, $2)`, subagentID, ghostID)
		Expect(err).NotTo(HaveOccurred())

		_, err = db.ExecContext(ctx, `
			INSERT INTO captain_session_processes
				(id, session_id, host_id, boot_id, pid, process_started_at, status)
			VALUES ($1, $2, 'host-a', 'boot-a', 42, now(), 'running')`, processID, ghostID)
		Expect(err).NotTo(HaveOccurred())

		Expect(Apply(ctx, dsn)).To(Succeed())

		var surviving int
		Expect(db.QueryRowContext(ctx,
			`SELECT count(*) FROM captain_sessions WHERE id = $1`, ghostID,
		).Scan(&surviving)).To(Succeed())
		Expect(surviving).To(Equal(0), "the ghost should have been collapsed into the winner")

		// The whole point: the subagent outlived the ghost, and now hangs off the row
		// that actually holds the transcript.
		var parent, root string
		Expect(db.QueryRowContext(ctx,
			`SELECT parent_session_id, root_session_id FROM captain_sessions WHERE id = $1`, subagentID,
		).Scan(&parent, &root)).To(Succeed(), "the subagent was cascaded away with the ghost")
		Expect(parent).To(Equal(winnerID))
		Expect(root).To(Equal(winnerID))

		var processSessionID string
		Expect(db.QueryRowContext(ctx,
			`SELECT session_id FROM captain_session_processes WHERE id = $1`, processID,
		).Scan(&processSessionID)).To(Succeed(), "the process row was cascaded away with the ghost")
		Expect(processSessionID).To(Equal(winnerID))

		// The winner keeps its transcript and absorbs the label that existed only on
		// the ghost.
		var messages int
		Expect(db.QueryRowContext(ctx,
			`SELECT count(*) FROM captain_messages WHERE session_id = $1`, winnerID,
		).Scan(&messages)).To(Succeed())
		Expect(messages).To(Equal(1))

		var provider string
		Expect(db.QueryRowContext(ctx,
			`SELECT provider FROM captain_sessions WHERE id = $1`, winnerID,
		).Scan(&provider)).To(Succeed())
		Expect(provider).To(Equal("codex-agent"))
	})
})
