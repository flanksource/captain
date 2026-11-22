package session

import (
	"strings"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A root session delegates to two sub-agents early on and keeps working long
// after both finish. The sub-agent whose file sorts last ran first.
const (
	orderRootJSONL = `{"type":"user","uuid":"prompt","sessionId":"sess-1","timestamp":"2026-07-14T10:00:00.000Z","message":{"role":"user","content":[{"type":"text","text":"Review the build."}]}}
{"type":"assistant","uuid":"delegate","sessionId":"sess-1","timestamp":"2026-07-14T10:01:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Delegating."}]}}
{"type":"assistant","uuid":"untimed","sessionId":"sess-1","message":{"role":"assistant","content":[{"type":"text","text":"Waiting on agents."}]}}
{"type":"assistant","uuid":"latest","sessionId":"sess-1","timestamp":"2026-07-14T12:00:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Still going."}]}}`
	orderAgentLateJSONL = `{"type":"user","uuid":"late-1","parentUuid":"delegate","sessionId":"sess-1","timestamp":"2026-07-14T10:03:00.000Z","message":{"role":"user","content":[{"type":"text","text":"Check tests."}]}}
{"type":"assistant","uuid":"late-2","sessionId":"sess-1","timestamp":"2026-07-14T10:04:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Tests pass."}]}}`
	orderAgentEarlyJSONL = `{"type":"user","uuid":"early-1","parentUuid":"delegate","sessionId":"sess-1","timestamp":"2026-07-14T10:01:30.000Z","message":{"role":"user","content":[{"type":"text","text":"Check lint."}]}}
{"type":"assistant","uuid":"early-2","sessionId":"sess-1","timestamp":"2026-07-14T10:02:00.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Lint clean."}]}}`
)

var _ = ginkgo.Describe("message order of a session with sub-agents", func() {
	transcript := func(path, jsonl string, agentID string) claude.ParsedTranscript {
		entries, err := claude.ReadHistory(strings.NewReader(jsonl))
		Expect(err).NotTo(HaveOccurred())
		return claude.ParsedTranscript{Path: path, Entries: entries, IsAgent: agentID != "", AgentID: agentID}
	}

	ginkgo.It("interleaves every transcript by time, so the newest root message is last", func() {
		s := buildSession(claude.ParsedSession{
			SessionID: "sess-1",
			Transcripts: []claude.ParsedTranscript{
				transcript("/p/sess-1.jsonl", orderRootJSONL, ""),
				transcript("/p/sess-1/subagents/agent-a.jsonl", orderAgentLateJSONL, "a"),
				transcript("/p/sess-1/subagents/agent-z.jsonl", orderAgentEarlyJSONL, "z"),
			},
		})

		ids := make([]string, len(s.Messages))
		for i, message := range s.Messages {
			ids[i] = message.ID
		}
		Expect(ids).To(Equal([]string{"prompt", "delegate", "untimed", "early-1", "early-2", "late-1", "late-2", "latest"}))
	})
})
