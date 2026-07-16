package database

import (
	"os"
	"path/filepath"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("Session list pages", func() {
	It("pages equal activity timestamps by id without loading detail aggregates", func(ctx SpecContext) {
		if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
			Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres store tests")
		}

		dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
			DataDir:  filepath.Join(GinkgoT().TempDir(), "postgres"),
			Database: "captain_session_list",
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(stop()).To(Succeed()) })

		db, err := Open(ctx, Config{DSN: dsn})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		activityAt := time.Date(2026, time.July, 16, 15, 0, 0, 0, time.UTC)
		ids := []uuid.UUID{
			uuid.MustParse("00000000-0000-0000-0000-000000000003"),
			uuid.MustParse("00000000-0000-0000-0000-000000000002"),
			uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		}
		for _, id := range ids {
			_, err = db.CreateOrGetSession(ctx, CreateSessionInput{
				ID: id, ProviderSessionID: "provider-" + id.String(), Source: "codex",
				Provider: "openai", HostID: "list-test", Project: "captain", CWD: "/work/captain",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(db.Gorm().Exec(
				"UPDATE captain_sessions SET started_at = ?, last_activity_at = ? WHERE id = ?",
				activityAt, activityAt, id,
			).Error).NotTo(HaveOccurred())
		}

		turnID := uuid.New()
		Expect(db.Gorm().Exec(
			"INSERT INTO captain_turns (id, session_id, turn_index) VALUES (?, ?, ?)",
			turnID, ids[0], 0,
		).Error).NotTo(HaveOccurred())
		Expect(db.Gorm().Exec(`
			INSERT INTO captain_model_calls
			  (turn_id, call_index, model, backend, input_tokens, output_tokens, context_tokens, context_window_tokens)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			turnID, 0, "gpt-5", "codex", 10, 5, 25, 100,
		).Error).NotTo(HaveOccurred())
		Expect(db.Gorm().Exec(
			"INSERT INTO captain_messages (session_id, sequence, role, parts) VALUES (?, ?, ?, ?::jsonb)",
			ids[0], 0, "assistant", `[{"type":"tool-shell","toolName":"shell"}]`,
		).Error).NotTo(HaveOccurred())

		first, err := db.ListSessionSummaries(ctx, SessionListFilter{RootsOnly: true, Limit: 2})
		Expect(err).NotTo(HaveOccurred())
		Expect(first.Total).To(Equal(int64(3)))
		Expect(first.Rows).To(HaveLen(2))
		Expect([]uuid.UUID{first.Rows[0].ID, first.Rows[1].ID}).To(Equal(ids[:2]))
		Expect(first.NextCursor).NotTo(BeEmpty())
		Expect(first.Rows[0]).To(MatchFields(IgnoreExtras, Fields{
			"Model":              PointTo(Equal("gpt-5")),
			"InputTokens":        Equal(int64(10)),
			"OutputTokens":       Equal(int64(5)),
			"ContextFreePercent": PointTo(Equal(75)),
		}))

		last, err := db.ListSessionSummaries(ctx, SessionListFilter{
			RootsOnly: true, Limit: 2, Cursor: first.NextCursor,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(last.Total).To(Equal(int64(3)))
		Expect(last.Rows).To(HaveLen(1))
		Expect(last.Rows[0].ID).To(Equal(ids[2]))
		Expect(last.NextCursor).To(BeEmpty())
	})

	It("keeps the list query independent of the aggregate overview", func() {
		Expect(sessionListQuery).NotTo(ContainSubstring("captain_session_overview"))
		Expect(sessionListQuery).NotTo(ContainSubstring("captain_messages"))
		Expect(sessionListQuery).NotTo(ContainSubstring("jsonb_array_elements"))
		Expect(sessionListQuery).NotTo(ContainSubstring("captain_events"))
		Expect(sessionListQuery).NotTo(ContainSubstring("captain_plans"))
		Expect(sessionListQuery).NotTo(ContainSubstring("captain_turn_requests"))
		Expect(sessionListQuery).NotTo(ContainSubstring("captain_artifacts"))
	})
})
