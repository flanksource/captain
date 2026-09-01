package database

import (
	"strings"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"gorm.io/gorm"
)

var _ = Describe("Thread-scoped session queries", Ordered, func() {
	var (
		db                *DB
		rootID            = uuid.MustParse("00000000-0000-0000-0000-0000000003a1")
		childID           = uuid.MustParse("00000000-0000-0000-0000-0000000003a2")
		transcriptID      = uuid.MustParse("00000000-0000-0000-0000-0000000003a3")
		transcriptChildID = uuid.MustParse("00000000-0000-0000-0000-0000000003a4")
		soloID            = uuid.MustParse("00000000-0000-0000-0000-0000000003b1")
		otherID           = uuid.MustParse("00000000-0000-0000-0000-0000000003c1")
	)

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_thread"})
		var err error
		db, err = Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		for _, session := range []struct {
			id       uuid.UUID
			rootID   *uuid.UUID
			parentID *uuid.UUID
			relation SessionParentRelation
		}{
			{id: rootID},
			{id: childID, rootID: &rootID, parentID: &rootID, relation: SessionParentRelationAgent},
			{id: transcriptID, rootID: &rootID, parentID: &rootID, relation: SessionParentRelationTranscript},
			{id: transcriptChildID, rootID: &rootID, parentID: &transcriptID, relation: SessionParentRelationAgent},
			{id: soloID},
			{id: otherID},
		} {
			_, err = db.CreateOrGetSession(ctx, CreateSessionInput{
				ID: session.id, RootSessionID: session.rootID, ParentSessionID: session.parentID,
				ParentRelation: session.relation, Source: "codex",
				Provider: "openai", HostID: "thread-test",
			})
			Expect(err).NotTo(HaveOccurred())
		}

		// occurred_at is nullable, so the ordering must interleave the root and its
		// subagent chronologically while parking un-timestamped rows at the end.
		Expect(db.Gorm().Exec(`
			INSERT INTO captain_messages (session_id, sequence, role, parts, occurred_at)
			VALUES
			  (?, 0, 'user',      '[]'::jsonb, '2026-07-16 10:00:00+00'),
			  (?, 1, 'assistant', '[]'::jsonb, NULL),
			  (?, 2, 'assistant', '[]'::jsonb, '2026-07-16 10:02:00+00'),
			  (?, 0, 'user',      '[]'::jsonb, '2026-07-16 10:01:00+00'),
			  (?, 0, 'assistant', '[]'::jsonb, '2026-07-16 10:01:10+00'),
			  (?, 0, 'assistant', '[]'::jsonb, '2026-07-16 10:01:20+00'),
			  (?, 0, 'user',      '[]'::jsonb, '2026-07-16 10:00:30+00'),
			  (?, 0, 'user',      '[]'::jsonb, '2026-07-16 10:00:40+00')`,
			rootID, rootID, rootID, childID, transcriptID, transcriptChildID, soloID, otherID,
		).Error).NotTo(HaveOccurred())
	})

	It("returns the root and agent children without duplicating transcript-owned messages", func(ctx SpecContext) {
		rows, err := db.ListThreadTranscriptMessages(ctx, rootID)

		Expect(err).NotTo(HaveOccurred())
		Expect(sessionSequences(rows)).To(Equal([]sessionSequence{
			{SessionID: rootID, Sequence: 0},  // 10:00:00
			{SessionID: childID, Sequence: 0}, // 10:01:00
			{SessionID: rootID, Sequence: 2},  // 10:02:00
			{SessionID: rootID, Sequence: 1},  // NULL occurred_at sorts last
		}))
	})

	It("returns only its own messages for a session with no subagents", func(ctx SpecContext) {
		rows, err := db.ListThreadTranscriptMessages(ctx, soloID)

		Expect(err).NotTo(HaveOccurred())
		Expect(sessionSequences(rows)).To(Equal([]sessionSequence{{SessionID: soloID, Sequence: 0}}))
	})

	It("resolves the thread through the session_id index rather than reading every message", func() {
		// `IN (subquery)` plans as a semi-join that Postgres can only apply as a
		// filter above the view's join, so captain_messages gets read in full
		// however narrow the thread is. The fixture is far too small for the
		// planner to prefer an index on cost, so seq scans are disabled and the
		// surviving plan reveals which access path exists at all.
		//
		// captain_messages_session_sequence_key is the only index that can answer
		// `session_id = …`, so naming it is the assertion that discriminates: under
		// the semi-join form this plan falls back to a full scan of an unrelated
		// index (captain_messages_model_call_id_idx) instead.
		Expect(explainThreadTranscript(db, rootID)).
			To(ContainSubstring("captain_messages_session_sequence_key"))
	})
})

func explainThreadTranscript(db *DB, rootID uuid.UUID) string {
	var lines []string
	Expect(db.Gorm().Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
			return err
		}
		rows, err := tx.Raw(
			"EXPLAIN SELECT * FROM captain_session_transcript WHERE "+threadScopePredicate+
				" ORDER BY occurred_at NULLS LAST, session_id, sequence",
			rootID, rootID, rootID,
		).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				return err
			}
			lines = append(lines, line)
		}
		return rows.Err()
	})).To(Succeed())
	return strings.Join(lines, "\n")
}

type sessionSequence struct {
	SessionID uuid.UUID
	Sequence  int64
}

func sessionSequences(rows []TranscriptMessage) []sessionSequence {
	keys := make([]sessionSequence, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, sessionSequence{SessionID: row.SessionID, Sequence: row.Sequence})
	}
	return keys
}
