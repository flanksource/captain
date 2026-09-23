package session

import (
	"strings"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// claudeInjectedSessionJSONL resumes a compacted session: Claude writes the
// compaction summary and a resume nudge as user lines, the person types a
// prompt, and later interrupts the reply.
const claudeInjectedSessionJSONL = `{"type":"user","uuid":"summary","sessionId":"sess-1","timestamp":"2026-07-14T12:16:09.000Z","cwd":"/repo","isCompactSummary":true,"message":{"role":"user","content":"This session is being continued from a previous conversation."}}
{"type":"user","uuid":"nudge","sessionId":"sess-1","timestamp":"2026-07-14T12:16:09.100Z","cwd":"/repo","isMeta":true,"message":{"role":"user","content":[{"type":"text","text":"Continue from where you left off."}]}}
{"type":"user","uuid":"prompt","sessionId":"sess-1","timestamp":"2026-07-14T12:16:10.000Z","cwd":"/repo","message":{"role":"user","content":[{"type":"text","text":"Ship the docker build."}]}}
{"type":"assistant","uuid":"reply","sessionId":"sess-1","timestamp":"2026-07-14T12:16:11.000Z","cwd":"/repo","message":{"role":"assistant","model":"claude-sonnet-4","content":[{"type":"text","text":"Starting."}]}}
{"type":"user","uuid":"interrupt","sessionId":"sess-1","timestamp":"2026-07-14T12:16:12.000Z","cwd":"/repo","message":{"role":"user","content":[{"type":"text","text":"[Request interrupted by user]"}]}}`

var _ = ginkgo.Describe("user lines Claude injected, in a built session", func() {
	var s *Session

	ginkgo.BeforeEach(func() {
		entries, err := claude.ReadHistory(strings.NewReader(claudeInjectedSessionJSONL))
		Expect(err).NotTo(HaveOccurred())
		s = buildSession(claude.ParsedSession{
			SessionID: "sess-1",
			Transcripts: []claude.ParsedTranscript{{
				Path: "/repo/.claude/sess-1.jsonl", Entries: entries, ToolUses: claude.ExtractToolUsesWithTokens(entries),
			}},
		})
	})

	ginkgo.It("keeps them visible as system messages, and only the typed prompt as the user's", func() {
		roles := map[string]string{}
		for _, message := range s.Messages {
			roles[message.ID] = message.Role
		}

		Expect(roles).To(Equal(map[string]string{
			"summary": "system", "nudge": "system", "prompt": "user", "reply": "assistant", "interrupt": "system",
		}))
	})

	ginkgo.It("takes the initial prompt from what the person typed", func() {
		Expect(s.InitialPrompt).To(Equal("Ship the docker build."))
	})
})
