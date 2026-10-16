package query

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session/transcript"
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

	It("keeps following the authoritative transcript after database ingestion starts", func(ctx SpecContext) {
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
			SessionID: transcript.ID, ProviderMessageID: "a1", Role: "assistant",
			Parts: json.RawMessage(`[{"type":"text","text":"written before ingest"}]`),
		})).To(Succeed())
		expectNoMessage(events)

		appendClaudeEntry(logPath, providerID, cwd, "a2", "written after ingest")
		Expect(bodies(nextMessages(events, 1))).To(Equal([]string{"written after ingest"}))

		childPath := filepath.Join(filepath.Dir(logPath), providerID, "subagents", "agent-new-child.jsonl")
		appendClaudeEntry(childPath, providerID, cwd, "child-1", "new sub-agent")
		Expect(bodies(nextMessages(events, 1))).To(Equal([]string{"new sub-agent"}))
		appendClaudeEntry(childPath, providerID, cwd, "child-2", "sub-agent continued")
		Expect(bodies(nextMessages(events, 1))).To(Equal([]string{"sub-agent continued"}))
	})

	DescribeTable("resumes after a sub-agent cursor and follows sub-agent writes",
		func(ctx SpecContext, ingested bool) {
			home := GinkgoT().TempDir()
			GinkgoT().Setenv("HOME", home)
			const cwd = "/work/follow_subagent"
			providerID := uuid.NewString()
			root, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
				ProviderSessionID: providerID, Source: "claude", Provider: "anthropic", HostID: "follow-tail", CWD: cwd,
			})
			Expect(err).NotTo(HaveOccurred())
			logPath := filepath.Join(home, ".claude", "projects", claude.NormalizePath(cwd), providerID+".jsonl")
			childPath := filepath.Join(filepath.Dir(logPath), providerID, "subagents", "agent-child.jsonl")
			appendClaudeEntry(logPath, providerID, cwd, "root-1", "root message")
			appendClaudeEntry(childPath, providerID, cwd, "child-1", "sub-agent cursor")
			if ingested {
				Expect(db.PutChatMessage(ctx, database.PutChatMessageInput{
					SessionID: root.ID, ProviderMessageID: "root-1", Role: "assistant",
					Parts: json.RawMessage(`[{"type":"text","text":"root message"}]`),
				})).To(Succeed())
			}
			parsed, err := transcript.Parse(transcript.Candidate{ID: providerID, Source: "claude", Path: logPath})
			Expect(err).NotTo(HaveOccurred())
			Expect(parsed.Messages).To(ContainElement(HaveField("ID", "child-1")))

			followCtx, cancel := context.WithCancel(ctx)
			events, err := Follow(followCtx, db, root.ID.String(), FollowOptions{Replay: true, After: "child-1"})
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() {
				cancel()
				Eventually(events, followWait).Should(BeClosed())
			})
			nextState(events)
			appendClaudeEntry(childPath, providerID, cwd, "child-2", "sub-agent update")
			Expect(bodies(nextMessages(events, 1))).To(Equal([]string{"sub-agent update"}))
			expectNoMessage(events)
		},
		Entry("before ingestion", false),
		Entry("with partially ingested parent messages", true),
	)

	// Claude Code writes ai-title lines without a uuid; streamed as-is they
	// became id-less entry frames the session inspector rejects.
	It("streams only conversational messages from the log, each with an id", func(ctx SpecContext) {
		home := GinkgoT().TempDir()
		GinkgoT().Setenv("HOME", home)
		const cwd = "/work/follow_tail_synthetic"
		providerID := uuid.NewString()
		launcher, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerID, Source: "gavel", Provider: "agent-claude", HostID: "follow-tail", CWD: cwd,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: providerID, Source: "claude", Provider: "anthropic", HostID: "follow-tail", CWD: cwd,
			ParentSessionID: &launcher.ID, RootSessionID: &launcher.ID, ParentRelation: database.SessionParentRelationTranscript,
		})
		Expect(err).NotTo(HaveOccurred())
		logPath := filepath.Join(home, ".claude", "projects", claude.NormalizePath(cwd), providerID+".jsonl")
		appendClaudeLine(logPath, map[string]any{"type": "ai-title", "aiTitle": "Untitled follow", "sessionId": providerID})
		appendClaudeEntry(logPath, providerID, cwd, "a1", "conversational")

		followCtx, cancel := context.WithCancel(ctx)
		events, err := Follow(followCtx, db, launcher.ID.String(), FollowOptions{Replay: true})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			cancel()
			Eventually(events, followWait).Should(BeClosed())
		})

		streamed := nextMessages(events, 1)
		Expect(streamed[0].Message.ID).To(Equal("a1"))
		Expect(bodies(streamed)).To(Equal([]string{"conversational"}))
		expectNoMessage(events)
	})
})

func appendClaudeEntry(path, sessionID, cwd, id, text string) {
	GinkgoHelper()
	appendClaudeLine(path, map[string]any{
		"type": "assistant", "sessionId": sessionID, "uuid": id, "cwd": cwd,
		"timestamp": "2026-09-24T10:00:00Z",
		"message": map[string]any{"id": id, "role": "assistant", "content": []any{
			map[string]any{"type": "text", "text": text},
		}},
	})
}

func appendClaudeLine(path string, line map[string]any) {
	GinkgoHelper()
	entry, err := json.Marshal(line)
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
