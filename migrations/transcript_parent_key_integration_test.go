package migrations

import (
	"context"
	"database/sql"

	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// One admission session is one provider conversation, so it holds at most one
// transcript child. captain_sessions_transcript_parent_key enforces that, and
// 04_demote_extra_transcript_children.sql repairs the rows written before it
// existed. 82_gavel_transcript_parent_relation.sql runs after the key is in
// place on an upgrade, so its relabel must not mint a second transcript either.
var _ = Describe("Captain transcript parent key", func() {
	const (
		parentID = "11111111-1111-1111-1111-111111111111"
		codexID  = "22222222-2222-2222-2222-222222222222"
		claudeID = "33333333-3333-3333-3333-333333333333"
		lateID   = "44444444-4444-4444-4444-444444444444"
	)

	// legacyDatabase is a migrated database put back into the state the key
	// forbids: the key dropped and the named repair scripts unrecorded, so the
	// next Apply runs them against the seeded rows.
	legacyDatabase := func(ctx context.Context, scripts ...string) (string, *sql.DB) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_transcript_parent_key"})
		dsn, db := handle.DSN(), handle.SQL()
		Expect(Apply(ctx, dsn)).To(Succeed())
		_, err := db.ExecContext(ctx, `DROP INDEX captain_sessions_transcript_parent_key`)
		Expect(err).NotTo(HaveOccurred())
		for _, script := range scripts {
			_, err = db.ExecContext(ctx,
				`DELETE FROM schema_migration_scripts WHERE scope = $1 AND path = $2`, Scope, script)
			Expect(err).NotTo(HaveOccurred())
		}
		_, err = db.ExecContext(ctx, `
			INSERT INTO captain_sessions (id, source, host_id, provider_session_id)
			VALUES ($1, 'gavel', 'host-a', 'rollout-1')`, parentID)
		Expect(err).NotTo(HaveOccurred())
		return dsn, db
	}

	seedChildren := func(ctx context.Context, db *sql.DB, relation string) {
		_, err := db.ExecContext(ctx, `
			INSERT INTO captain_sessions (id, source, host_id, provider_session_id, parent_session_id, root_session_id, parent_relation, created_at)
			VALUES ($1, 'codex',  'host-a', 'rollout-1', $3, $3, $4, now() - interval '2 minutes'),
			       ($2, 'claude', 'host-a', 'rollout-1', $3, $3, $4, now() - interval '1 minute')`,
			codexID, claudeID, parentID, relation)
		Expect(err).NotTo(HaveOccurred())
	}

	relations := func(ctx context.Context, db *sql.DB) map[string]string {
		rows, err := db.QueryContext(ctx,
			`SELECT id::text, parent_relation FROM captain_sessions WHERE parent_session_id = $1`, parentID)
		Expect(err).NotTo(HaveOccurred())
		defer func() { Expect(rows.Close()).To(Succeed()) }()
		result := map[string]string{}
		for rows.Next() {
			var id, relation string
			Expect(rows.Scan(&id, &relation)).To(Succeed())
			result[id] = relation
		}
		Expect(rows.Err()).NotTo(HaveOccurred())
		return result
	}

	insertLateTranscript := func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `
			INSERT INTO captain_sessions (id, source, host_id, provider_session_id, parent_session_id, root_session_id, parent_relation)
			VALUES ($1, 'api', 'host-a', 'rollout-1', $2, $2, 'transcript')`, lateID, parentID)
		return err
	}

	It("keeps the oldest transcript child, demotes the rest to agent, and rejects another", func(ctx SpecContext) {
		dsn, db := legacyDatabase(ctx, "04_demote_extra_transcript_children.sql")
		seedChildren(ctx, db, "transcript")

		Expect(Apply(ctx, dsn)).To(Succeed())

		Expect(relations(ctx, db)).To(Equal(map[string]string{codexID: "transcript", claudeID: "agent"}))
		Expect(insertLateTranscript(ctx, db)).To(MatchError(ContainSubstring("captain_sessions_transcript_parent_key")))
	})

	It("relabels only the oldest Gavel agent child when the key already exists", func(ctx SpecContext) {
		dsn, db := legacyDatabase(ctx, "04_demote_extra_transcript_children.sql", "82_gavel_transcript_parent_relation.sql")
		seedChildren(ctx, db, "agent")

		Expect(Apply(ctx, dsn)).To(Succeed())

		Expect(relations(ctx, db)).To(Equal(map[string]string{codexID: "transcript", claudeID: "agent"}))
	})
})
