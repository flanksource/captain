package cli

import (
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/session"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/segmentio/encoding/json"
)

// claudeGoalTranscript is the exact three-record shape Claude writes for a
// `/goal` interaction: a goal_status attachment, the slash-command wrapper as a
// text-only user message, and its captured stdout.
const claudeGoalTranscript = `{"type":"attachment","uuid":"uuid-goal","sessionId":"s1","timestamp":"2026-07-14T12:16:09.113Z","cwd":"/repo","attachment":{"type":"goal_status","met":false,"sentinel":true,"condition":"ship the docker build"}}
{"type":"user","uuid":"uuid-cmd","sessionId":"s1","timestamp":"2026-07-14T12:16:09.200Z","cwd":"/repo","message":{"role":"user","content":"<command-name>/goal</command-name>\n            <command-message>goal</command-message>\n            <command-args>ship the docker build</command-args>"}}
{"type":"user","uuid":"uuid-out","sessionId":"s1","timestamp":"2026-07-14T12:16:09.300Z","cwd":"/repo","message":{"role":"user","content":"<local-command-stdout>Goal set: ship the docker build</local-command-stdout>"}}`

const claudeWrappedShellTranscript = `{"type":"assistant","sessionId":"s1","uuid":"uuid-bash","timestamp":"2026-07-14T12:16:09.300Z","cwd":"/repo","message":{"role":"assistant","content":[{"type":"tool_use","id":"tool-1","name":"Bash","input":{"command":"/bin/zsh -lc 'pnpm test'"}}]}}`

func historyToolNames(uses []claude.ToolUse) []string {
	names := make([]string, 0, len(uses))
	for _, u := range uses {
		names = append(names, u.Tool)
	}
	return names
}

var _ = Describe("Claude /goal transcript history from a reader", func() {
	It("detects the input as Claude JSONL and structures the command records", func() {
		res, err := parseFromReader([]byte(claudeGoalTranscript))
		Expect(err).NotTo(HaveOccurred())
		Expect(res.Format).To(Equal(claude.FormatClaudeJSONL))

		Expect(historyToolNames(res.ToolUses)).To(Equal([]string{"GoalStatus", "ClaudeCommand", "ClaudeCommand"}))

		for _, u := range res.ToolUses {
			Expect(u.Tool).NotTo(Equal("User"))
			if text, ok := u.Input["text"].(string); ok {
				Expect(text).NotTo(ContainSubstring("<command-name>"))
				Expect(text).NotTo(ContainSubstring("<local-command-stdout>"))
			}
		}
	})

	It("renders structured GoalStatus/ClaudeCommand rows and no raw wrapper rows under a normal limit", func() {
		out, err := runHistoryFromReader([]byte(claudeGoalTranscript), HistoryOptions{Limit: 12})
		Expect(err).NotTo(HaveOccurred())

		result, ok := out.(session.HistoryResult)
		Expect(ok).To(BeTrue())

		rowTools := make([]string, 0, len(result.Results))
		var goalSummary, cmdSummary, outSummary string
		for _, row := range result.Results {
			rowTools = append(rowTools, row.Tool)
			switch {
			case row.Tool == "GoalStatus":
				goalSummary = row.Summary
			case row.Tool == "ClaudeCommand" && cmdSummary == "":
				cmdSummary = row.Summary
			case row.Tool == "ClaudeCommand":
				outSummary = row.Summary
			}
			Expect(row.Summary).NotTo(ContainSubstring("<command-name>"))
			Expect(row.Summary).NotTo(ContainSubstring("<local-command-stdout>"))
		}

		Expect(rowTools).To(ContainElement("GoalStatus"))
		Expect(rowTools).To(ContainElement("ClaudeCommand"))
		Expect(rowTools).NotTo(ContainElement("User"))

		Expect(goalSummary).To(ContainSubstring("ship the docker build"))
		Expect(cmdSummary).To(ContainSubstring("/goal"))
		Expect(outSummary).To(ContainSubstring("Goal set: ship the docker build"))
	})

	It("serializes transformed shell input unless raw history was requested", func() {
		out, err := runHistoryFromReader([]byte(claudeWrappedShellTranscript), HistoryOptions{Limit: 1})
		Expect(err).NotTo(HaveOccurred())
		encoded, err := json.Marshal(out)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(encoded)).To(And(
			ContainSubstring("zsh"),
			ContainSubstring("pnpm test"),
			Not(ContainSubstring("/bin/zsh -lc")),
		))

		rawOut, err := runHistoryFromReader([]byte(claudeWrappedShellTranscript), HistoryOptions{Limit: 1, Raw: true})
		Expect(err).NotTo(HaveOccurred())
		rawEncoded, err := json.Marshal(rawOut)
		Expect(err).NotTo(HaveOccurred())
		Expect(string(rawEncoded)).To(ContainSubstring(`/bin/zsh -lc`))
	})
})
