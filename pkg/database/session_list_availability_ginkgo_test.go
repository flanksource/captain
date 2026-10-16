package database

import (
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Session list availability", func() {
	It("filters inactive empty sessions before totals and paging while retaining activity and content", func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_availability"})
		db, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })

		kinds := []string{"process", "ask", "approval", "path", "source", "messages", "prompt", "run-owner", "run-execution", "metadata", "whitespace", "ended-process"}
		ids := make([]uuid.UUID, len(kinds))
		var linkedRun *PromptRun
		activityAt := time.Date(2026, time.October, 11, 12, 0, 0, 0, time.UTC)
		for index, kind := range kinds {
			input := CreateSessionInput{
				Source: "claude", Provider: "anthropic", HostID: "availability-test", Title: kind, CWD: "/work/example",
			}
			if kind == "run-owner" {
				input.Source, input.ProviderSessionID = "gavel", "linked-run"
			}
			if kind == "run-execution" {
				input.ProviderSessionID = "linked-run"
				input.ParentSessionID, input.RootSessionID = &ids[7], &ids[7]
				input.ParentRelation = SessionParentRelationTranscript
			}
			row, err := db.CreateOrGetSession(ctx, input)
			Expect(err).NotTo(HaveOccurred())
			ids[index] = row.ID
			Expect(db.Gorm().Exec("UPDATE captain_sessions SET last_activity_at = ? WHERE id = ?", activityAt.Add(time.Duration(index)*time.Minute), row.ID).Error).To(Succeed())
			switch kind {
			case "process", "ended-process":
				Expect(db.UpsertSessionProcess(ctx, SessionProcessInput{SessionID: row.ID, HostID: "availability-test", BootID: "boot", PID: int64(1000 + index), ProcessStartedAt: activityAt, Status: "active", Source: "claude"})).To(Succeed())
				if kind == "ended-process" {
					Expect(db.Gorm().Exec("UPDATE captain_session_processes SET ended_at = ? WHERE session_id = ?", activityAt, row.ID).Error).To(Succeed())
				}
			case "ask", "approval":
				Expect(db.Gorm().Exec("UPDATE captain_sessions SET activity_state = ? WHERE id = ?", kind, row.ID).Error).To(Succeed())
			case "path":
				Expect(db.Gorm().Exec("UPDATE captain_sessions SET path = ? WHERE id = ?", "/work/example/transcript.jsonl", row.ID).Error).To(Succeed())
			case "source":
				Expect(db.RegisterTranscriptSource(ctx, row.ID, "claude", "/work/example/registered.jsonl", "registered")).To(Succeed())
			case "messages":
				Expect(db.Gorm().Exec("INSERT INTO captain_messages (session_id, sequence, role, parts) VALUES (?, 0, 'assistant', ?::jsonb)", row.ID, `[{"type":"text","text":"Stored answer"}]`).Error).To(Succeed())
			case "prompt":
				Expect(db.Gorm().Exec("UPDATE captain_sessions SET initial_prompt = ? WHERE id = ?", "Inspect the repository", row.ID).Error).To(Succeed())
			case "run-owner":
				linkedRun, err = db.CreatePromptRun(ctx, CreatePromptRunInput{SessionID: row.ID})
				Expect(err).NotTo(HaveOccurred())
			case "run-execution":
				_, err = db.UpdatePromptRun(ctx, UpdatePromptRunInput{ID: linkedRun.ID, ExpectedVersion: linkedRun.Version, ExecutionSessionID: &row.ID})
				Expect(err).NotTo(HaveOccurred())
			case "whitespace":
				Expect(db.Gorm().Exec("UPDATE captain_sessions SET path = '   ', initial_prompt = '   ' WHERE id = ?", row.ID).Error).To(Succeed())
			}
		}

		unfiltered, err := db.ListSessionSummaries(ctx, SessionListFilter{IncludeTotal: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(unfiltered.Total).To(Equal(int64(len(kinds))))
		var actual []uuid.UUID
		filter := SessionListFilter{ExcludeInactiveEmpty: true, Limit: 2, IncludeTotal: true}
		for {
			page, err := db.ListSessionSummaries(ctx, filter)
			Expect(err).NotTo(HaveOccurred())
			Expect(page.Total).To(Equal(int64(9)))
			for _, row := range page.Rows {
				actual = append(actual, row.ID)
			}
			if page.NextCursor == "" {
				break
			}
			Expect(page.Rows).To(HaveLen(filter.Limit))
			filter.Cursor = page.NextCursor
		}
		Expect(actual).To(Equal([]uuid.UUID{ids[8], ids[7], ids[6], ids[5], ids[4], ids[3], ids[2], ids[1], ids[0]}))
	})
})
