package session

import (
	"strings"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// claudeGoalSessionJSONL is a minimal session that opens with the three-record
// /goal shape (goal_status attachment, slash-command wrapper, stdout) followed
// by a real user prompt and an assistant reply.
const claudeGoalSessionJSONL = `{"type":"attachment","uuid":"g","sessionId":"sess-1","timestamp":"2026-07-14T12:16:09.113Z","cwd":"/repo","attachment":{"type":"goal_status","met":false,"sentinel":true,"condition":"ship the docker build"}}
{"type":"user","uuid":"c","sessionId":"sess-1","timestamp":"2026-07-14T12:16:09.200Z","cwd":"/repo","message":{"role":"user","content":"<command-name>/goal</command-name>\n            <command-message>goal</command-message>\n            <command-args>ship the docker build</command-args>"}}
{"type":"user","uuid":"o","sessionId":"sess-1","timestamp":"2026-07-14T12:16:09.300Z","cwd":"/repo","message":{"role":"user","content":"<local-command-stdout>Goal set: ship the docker build</local-command-stdout>"}}
{"type":"user","uuid":"p","sessionId":"sess-1","timestamp":"2026-07-14T12:16:10.000Z","cwd":"/repo","message":{"role":"user","content":[{"type":"text","text":"Acknowledge the goal and start on the docker build."}]}}
{"type":"assistant","uuid":"a","sessionId":"sess-1","timestamp":"2026-07-14T12:16:11.000Z","cwd":"/repo","message":{"role":"assistant","model":"claude-sonnet-4","content":[{"type":"text","text":"On it."}],"stop_reason":"end_turn"}}`

func buildGoalSession() *Session {
	entries, err := claude.ReadHistory(strings.NewReader(claudeGoalSessionJSONL))
	Expect(err).NotTo(HaveOccurred())
	ps := claude.ParsedSession{
		SessionID: "sess-1",
		Transcripts: []claude.ParsedTranscript{{
			Path:     "/repo/.claude/sess-1.jsonl",
			Entries:  entries,
			ToolUses: claude.ExtractToolUsesWithTokens(entries),
		}},
	}
	return buildSession(ps)
}

func allTurnEventTypes(s *Session) []string {
	var types []string
	for _, turn := range s.Turns {
		for _, ev := range turn.Events {
			types = append(types, ev.Type)
		}
	}
	return types
}

var _ = ginkgo.Describe("Claude /goal records in a built session", func() {
	ginkgo.It("records goal_status as a session-scoped event, not a message", func() {
		s := buildGoalSession()

		var goal *Event
		for i := range s.Events {
			if s.Events[i].Type == "goal_status" {
				goal = &s.Events[i]
			}
		}
		Expect(goal).NotTo(BeNil())
		Expect(goal.Scope).To(Equal("session"))
		Expect(goal.Data["condition"]).To(Equal("ship the docker build"))
	})

	ginkgo.It("records command and output as turn-scoped events", func() {
		s := buildGoalSession()
		Expect(allTurnEventTypes(s)).To(ContainElements("claude_command", "claude_command_output"))
	})

	ginkgo.It("does not count command/goal records as approvals or denials", func() {
		s := buildGoalSession()
		Expect(s.Approvals.Approved).To(Equal(0))
		Expect(s.Approvals.Denied).To(Equal(0))
	})

	ginkgo.It("keeps the real user prompt as the initial prompt and title source", func() {
		s := buildGoalSession()
		Expect(s.InitialPrompt).To(Equal("Acknowledge the goal and start on the docker build."))
		Expect(s.Title).NotTo(ContainSubstring("<command-name>"))
		Expect(s.Title).NotTo(ContainSubstring("command-args"))
	})

	ginkgo.It("never surfaces raw command markup as conversational message text", func() {
		s := buildGoalSession()
		var userPromptSeen bool
		for _, m := range s.Messages {
			for _, p := range m.Parts {
				Expect(p.Text).NotTo(ContainSubstring("<command-name>"))
				Expect(p.Text).NotTo(ContainSubstring("<local-command-stdout>"))
				if m.Role == "user" && strings.Contains(p.Text, "Acknowledge the goal") {
					userPromptSeen = true
				}
			}
		}
		Expect(userPromptSeen).To(BeTrue())
	})
})
