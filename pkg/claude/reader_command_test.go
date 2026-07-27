package claude

import (
	"fmt"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/claude/tools"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/segmentio/encoding/json"
)

func TestClaudeCommandParsing(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Claude Command Parsing Suite")
}

// jsonString encodes s as a JSON string literal for embedding in a JSONL line.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// userLine builds a modern text-only user record whose message content is text.
func userLine(uuid, text string) string {
	return fmt.Sprintf(
		`{"type":"user","uuid":%q,"sessionId":"s1","timestamp":"2026-07-14T12:16:09.113Z","cwd":"/repo","gitBranch":"main","message":{"role":"user","content":%s}}`,
		uuid, jsonString(text))
}

// systemLocalCommandLine builds the older system/local_command record shape.
func systemLocalCommandLine(uuid, content string) string {
	return fmt.Sprintf(
		`{"type":"system","subtype":"local_command","uuid":%q,"sessionId":"s1","timestamp":"2026-07-14T12:16:09.113Z","content":%s,"level":"info"}`,
		uuid, jsonString(content))
}

func goalStatusLine(uuid, condition string, met, sentinel bool) string {
	return fmt.Sprintf(
		`{"type":"attachment","uuid":%q,"sessionId":"s1","timestamp":"2026-07-14T12:16:09.113Z","cwd":"/repo","attachment":{"type":"goal_status","met":%t,"sentinel":%t,"condition":%s}}`,
		uuid, met, sentinel, jsonString(condition))
}

// commandWrapper renders the slash-command envelope with the realistic newline
// and indentation Claude writes between sections.
func commandWrapper(name, message, args string) string {
	return fmt.Sprintf(
		"<command-name>%s</command-name>\n            <command-message>%s</command-message>\n            <command-args>%s</command-args>",
		name, message, args)
}

func stdoutWrapper(content string) string {
	return "<local-command-stdout>" + content + "</local-command-stdout>"
}

func stderrWrapper(content string) string {
	return "<local-command-stderr>" + content + "</local-command-stderr>"
}

func readEntries(lines ...string) []HistoryEntry {
	entries, err := ReadHistory(strings.NewReader(strings.Join(lines, "\n")))
	Expect(err).NotTo(HaveOccurred())
	return entries
}

var _ = Describe("Claude command / goal parsing in the shared reader", func() {
	const goalCondition = "push and monitor the docker build on PR #32, if any dependencies need updates open PR's for them and pin to the git SHA until they merge into main and create a release"

	Describe("the exact reported /goal three-record shape", func() {
		var entries []HistoryEntry

		BeforeEach(func() {
			entries = readEntries(
				goalStatusLine("uuid-goal", goalCondition, false, true),
				userLine("uuid-cmd", commandWrapper("/goal", "goal", goalCondition)),
				userLine("uuid-out", stdoutWrapper("Goal set: "+goalCondition)),
			)
		})

		It("surfaces goal_status as a session-scoped event preserving its fields", func() {
			Expect(entries).To(HaveLen(3))
			ev := entries[0].Event
			Expect(ev).NotTo(BeNil())
			Expect(ev.Type).To(Equal("goal_status"))
			Expect(ev.Scope).To(Equal("session"))
			Expect(ev.Data["condition"]).To(Equal(goalCondition))
			Expect(ev.Data["met"]).To(Equal(false))
			Expect(ev.Data["sentinel"]).To(Equal(true))
		})

		It("parses the /goal record into a claude_command event with split fields", func() {
			ev := entries[1].Event
			Expect(ev).NotTo(BeNil())
			Expect(ev.Type).To(Equal("claude_command"))
			Expect(ev.Scope).To(Equal("turn"))
			Expect(ev.Data["command_name"]).To(Equal("/goal"))
			Expect(ev.Data["command_message"]).To(Equal("goal"))
			Expect(ev.Data["command_args"]).To(Equal(goalCondition))
		})

		It("parses the stdout record into a wrapper-free claude_command_output event", func() {
			ev := entries[2].Event
			Expect(ev).NotTo(BeNil())
			Expect(ev.Type).To(Equal("claude_command_output"))
			Expect(ev.Data["stream"]).To(Equal("stdout"))
			Expect(ev.Data["content"]).To(Equal("Goal set: " + goalCondition))
		})

		It("emits no raw User text carrying the wrapper tags", func() {
			for _, e := range entries {
				Expect(e.Message.Role).To(BeEmpty())
				Expect(e.Message.GetTextContent()).NotTo(ContainSubstring("<command-name>"))
				Expect(e.Message.GetTextContent()).NotTo(ContainSubstring("<local-command-stdout>"))
			}
		})

		It("projects the three records as non-operational event activity", func() {
			uses := ExtractToolUses(entries)
			names := make([]string, 0, len(uses))
			for _, u := range uses {
				names = append(names, u.Tool)
				Expect(tools.IsEventToolName(u.Tool)).To(BeTrue(), "expected %q to be an event tool", u.Tool)
			}
			Expect(names).To(Equal([]string{"GoalStatus", "ClaudeCommand", "ClaudeCommand"}))
		})
	})

	DescribeTable("command wrappers across command names and argument shapes",
		func(line string, wantName, wantMessage, wantArgs string) {
			entries := readEntries(line)
			Expect(entries).To(HaveLen(1))
			ev := entries[0].Event
			Expect(ev).NotTo(BeNil())
			Expect(ev.Type).To(Equal("claude_command"))
			Expect(ev.Scope).To(Equal("turn"))
			Expect(ev.Data["command_name"]).To(Equal(wantName))
			Expect(ev.Data["command_message"]).To(Equal(wantMessage))
			Expect(ev.Data["command_args"]).To(Equal(wantArgs))
		},
		Entry("/plan with empty arguments (modern user shape)",
			userLine("u1", commandWrapper("/plan", "plan", "")), "/plan", "plan", ""),
		Entry("/clear with empty arguments (modern user shape)",
			userLine("u2", commandWrapper("/clear", "clear", "")), "/clear", "clear", ""),
		Entry("/effort with non-empty arguments (modern user shape)",
			userLine("u3", commandWrapper("/effort", "effort", "high")), "/effort", "effort", "high"),
		Entry("/usage via the older system/local_command shape",
			systemLocalCommandLine("u4", commandWrapper("/usage", "usage", "")), "/usage", "usage", ""),
		Entry("multiline arguments preserved verbatim",
			userLine("u5", commandWrapper("/goal", "goal", "line1\nline2")), "/goal", "goal", "line1\nline2"),
	)

	DescribeTable("output wrappers across streams and shapes",
		func(line, wantStream, wantContent string) {
			entries := readEntries(line)
			Expect(entries).To(HaveLen(1))
			ev := entries[0].Event
			Expect(ev).NotTo(BeNil())
			Expect(ev.Type).To(Equal("claude_command_output"))
			Expect(ev.Scope).To(Equal("turn"))
			Expect(ev.Data["stream"]).To(Equal(wantStream))
			Expect(ev.Data["content"]).To(Equal(wantContent))
		},
		Entry("empty stdout (modern user shape)",
			userLine("o1", stdoutWrapper("")), "stdout", ""),
		Entry("empty stdout (older system/local_command shape)",
			systemLocalCommandLine("o2", stdoutWrapper("")), "stdout", ""),
		Entry("non-empty stdout (older system/local_command shape)",
			systemLocalCommandLine("o3", stdoutWrapper("MCP dialog dismissed")), "stdout", "MCP dialog dismissed"),
		Entry("stderr stream",
			userLine("o4", stderrWrapper("boom")), "stderr", "boom"),
		Entry("multiline output with ANSI escapes preserved",
			userLine("o5", stdoutWrapper("\x1b[31mred\x1b[0m\nsecond line")), "stdout", "\x1b[31mred\x1b[0m\nsecond line"),
	)

	Describe("false positives and malformed wrappers", func() {
		It("leaves ordinary prose that merely mentions a tag as a User message", func() {
			text := "Please review how <command-name> is parsed in the reader."
			entries := readEntries(userLine("p1", text))
			Expect(entries).To(HaveLen(1))
			Expect(entries[0].Event).To(BeNil())
			Expect(entries[0].Message.Role).To(Equal(MessageRoleUser))
			Expect(entries[0].Message.GetTextContent()).To(Equal(text))
		})

		It("does not classify mixed content that is not a single text block", func() {
			line := fmt.Sprintf(
				`{"type":"user","uuid":"m1","sessionId":"s1","timestamp":"2026-07-14T12:16:09.113Z","message":{"role":"user","content":[{"type":"text","text":%s},{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
				jsonString(commandWrapper("/goal", "goal", "do things")))
			entries := readEntries(line)
			Expect(entries).To(HaveLen(1))
			Expect(entries[0].Event).To(BeNil())
			Expect(entries[0].Message.Role).To(Equal(MessageRoleUser))
		})

		It("surfaces a recognized-but-incomplete wrapper as a ParseError without stopping the read", func() {
			partial := "<command-name>/goal</command-name>" // missing message + args sections
			entries := readEntries(
				userLine("bad", partial),
				userLine("good", "a normal follow-up prompt"),
			)
			Expect(entries).To(HaveLen(2))

			parseErr := entries[0].Message.GetToolUses()
			Expect(parseErr).To(HaveLen(1))
			Expect(parseErr[0].Name).To(Equal("ParseError"))

			Expect(entries[1].Event).To(BeNil())
			Expect(entries[1].Message.Role).To(Equal(MessageRoleUser))
			Expect(entries[1].Message.GetTextContent()).To(Equal("a normal follow-up prompt"))
		})
	})
})
