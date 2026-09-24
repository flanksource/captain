package query

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A launcher books its transcript row before the provider writes a line, but
// captain_messages only fills once a monitor ingests the log, and no monitor
// may be running. Until then the log file is the only source.
var _ = Describe("Follow before the transcript is ingested", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_session_follow_tail"})
		var err error
		db, err = database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(db.Close()).To(Succeed()) })
	})

	It("finds a transcript row booked after it started and tails its log until the database carries messages", func(ctx SpecContext) {
		home := GinkgoT().TempDir()
		GinkgoT().Setenv("HOME", home)
		const cwd = "/work/follow_tail"
		providerID := uuid.NewString()
		launcher, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerID, Source: "gavel", Provider: "agent-claude", HostID: "follow-tail", CWD: cwd,
		})
		Expect(err).NotTo(HaveOccurred())
		logPath := filepath.Join(home, ".claude", "projects", claude.NormalizePath(cwd), providerID+".jsonl")

		followCtx, cancel := context.WithCancel(ctx)
		events, err := Follow(followCtx, db, launcher.ID.String(), FollowOptions{Replay: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			cancel()
			Eventually(events, followWait).Should(BeClosed())
		})
		nextState(events)

		transcript, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerID, Source: "claude", Provider: "anthropic", HostID: "follow-tail", CWD: cwd,
			ParentSessionID: &launcher.ID, RootSessionID: &launcher.ID, ParentRelation: database.SessionParentRelationTranscript,
		})
		Expect(err).NotTo(HaveOccurred())
		appendClaudeEntry(logPath, providerID, cwd, "a1", "written before ingest")
		Expect(bodies(nextMessages(events, 1))).To(Equal([]string{"written before ingest"}))

		Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
			SessionID: transcript.ID, ProviderMessageID: "db-1", Role: "assistant",
			Parts: json.RawMessage(`[{"type":"text","text":"ingested"}]`),
		})).To(Succeed())
		Expect(bodies(nextMessages(events, 1))).To(Equal([]string{"ingested"}))

		appendClaudeEntry(logPath, providerID, cwd, "a2", "the log is no longer followed")
		expectNoMessage(events)
	})
})

func appendClaudeEntry(path, sessionID, cwd, id, text string) {
	GinkgoHelper()
	entry, err := json.Marshal(map[string]any{
		"type": "assistant", "sessionId": sessionID, "uuid": id, "cwd": cwd,
		"timestamp": "2026-09-24T10:00:00Z",
		"message": map[string]any{"id": id, "role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": text},
		}},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(os.MkdirAll(filepath.Dir(path), 0o755)).To(Succeed())
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	Expect(err).NotTo(HaveOccurred())
	_, err = file.Write(append(entry, '\n'))
	Expect(err).NotTo(HaveOccurred())
	Expect(file.Close()).To(Succeed())
}

func bodies(events []FollowEvent) []string {
	texts := make([]string, 0, len(events))
	for _, event := range events {
		var parts []string
		for _, part := range event.Message.Parts {
			parts = append(parts, part.Text)
		}
		texts = append(texts, strings.Join(parts, ""))
	}
	return texts
}
