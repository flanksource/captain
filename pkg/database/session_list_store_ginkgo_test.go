package database

import (
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

var _ = Describe("Session list pages", func() {
	It("pages equal activity timestamps by id without loading detail aggregates", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_list"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
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
			  (turn_id, call_index, model, provider, mode, input_tokens, output_tokens, context_tokens, context_window_tokens)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			turnID, 0, "gpt-5", "openai", "cli", 10, 5, 25, 100,
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
			"MessageCount":       Equal(int64(1)),
			"ToolCallCount":      Equal(int64(1)),
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

	It("combines inclusive activity bounds with live-only filtering", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_range"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		from := time.Date(2026, time.July, 26, 10, 0, 0, 0, time.UTC)
		before := from.Add(2 * time.Hour)
		activityTimes := []time.Time{
			from.Add(-time.Hour),
			from,
			from.Add(time.Hour),
			before,
		}
		ids := make([]uuid.UUID, 0, len(activityTimes))
		for index, activityAt := range activityTimes {
			record, err := db.CreateOrGetSession(ctx, CreateSessionInput{
				ID: uuid.New(), ProviderSessionID: activityAt.Format(time.RFC3339), Source: "codex",
				Provider: "openai", HostID: "range-test", Project: "captain", CWD: "/work/captain",
			})
			Expect(err).NotTo(HaveOccurred())
			ids = append(ids, record.ID)
			Expect(db.Gorm().Exec(
				"UPDATE captain_sessions SET started_at = ?, last_activity_at = ? WHERE id = ?",
				activityAt, activityAt, record.ID,
			).Error).NotTo(HaveOccurred())
			if index == 1 {
				Expect(db.UpsertSessionProcess(ctx, SessionProcessInput{
					SessionID: record.ID, HostID: "range-test", BootID: "boot", PID: 4821,
					ProcessStartedAt: activityAt, Status: "active", CWD: "/work/captain", Source: "codex",
				})).To(Succeed())
			}
		}

		bounded, err := db.ListSessionSummaries(ctx, SessionListFilter{
			RootsOnly: true, ActivityFrom: &from, ActivityBefore: &before,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(bounded.Total).To(Equal(int64(2)))
		Expect([]uuid.UUID{bounded.Rows[0].ID, bounded.Rows[1].ID}).To(Equal([]uuid.UUID{ids[2], ids[1]}))

		live, err := db.ListSessionSummaries(ctx, SessionListFilter{
			RootsOnly: true, LiveOnly: true, ActivityFrom: &from, ActivityBefore: &before,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(live.Total).To(Equal(int64(1)))
		Expect(live.Rows).To(HaveLen(1))
		Expect(live.Rows[0].ID).To(Equal(ids[1]))
	})

	It("keeps detail aggregates bounded to paged sessions", func() {
		Expect(sessionListQuery).NotTo(ContainSubstring("captain_session_overview"))
		Expect(sessionListQuery).To(ContainSubstring("JOIN paged p ON p.id = m.session_id"))
		Expect(sessionListQuery).NotTo(ContainSubstring("captain_events"))
		Expect(sessionListQuery).NotTo(ContainSubstring("captain_plans"))
		Expect(sessionListQuery).NotTo(ContainSubstring("captain_turn_requests"))
		Expect(sessionListQuery).NotTo(ContainSubstring("captain_artifacts"))
	})
})
