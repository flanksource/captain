package history

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

func TestCodexNormalization(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Codex Normalization Suite")
}

var _ = Describe("NormalizeCodexToolCall", func() {
	It("normalizes command calls from every Codex transport to Bash", func() {
		uses := []ToolUse{
			NormalizeCodexToolCall(CodexToolCall{
				Name:      "shell",
				Arguments: []byte(`{"command":"pwd"}`),
				ID:        "call-1",
				SessionID: "thread-1",
			}),
			NormalizeCodexToolCall(CodexToolCall{
				Command:   "pwd",
				ID:        "call-1",
				SessionID: "thread-1",
			}),
		}

		Expect(uses).To(ConsistOf(
			MatchFields(IgnoreExtras, Fields{
				"Tool":      Equal("Bash"),
				"Input":     Equal(map[string]any{"command": "pwd"}),
				"ToolUseID": Equal("call-1"),
				"SessionID": Equal("thread-1"),
				"Source":    Equal("codex"),
			}),
			MatchFields(IgnoreExtras, Fields{
				"Tool":      Equal("Bash"),
				"Input":     Equal(map[string]any{"command": "pwd"}),
				"ToolUseID": Equal("call-1"),
				"SessionID": Equal("thread-1"),
				"Source":    Equal("codex"),
			}),
		))
	})

	It("normalizes a Codex exec wrapper to Bash without losing its outer metadata", func() {
		script := `const r = await tools.exec_command({
  cmd: "printf 'hello\\n'",
  workdir: "/repo/work"
}); text(r.output);`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:       "exec",
			Input:      map[string]any{"input": script},
			CWD:        "/repo",
			ID:         "call-exec",
			SessionID:  "session-1",
			TurnID:     "turn-1",
			Response:   "hello",
			RecordType: "response_item.custom_tool_call",
		})

		Expect(use).To(MatchFields(IgnoreExtras, Fields{
			"Tool":       Equal("Bash"),
			"Input":      Equal(map[string]any{"command": "printf 'hello\\n'", "input": script}),
			"CWD":        Equal("/repo/work"),
			"ToolUseID":  Equal("call-exec"),
			"SessionID":  Equal("session-1"),
			"TurnID":     Equal("turn-1"),
			"Response":   Equal("hello"),
			"RecordType": Equal("response_item.custom_tool_call"),
		}))
	})

	It("combines composed exec commands in source order and keeps dynamic expressions visible", func() {
		script := `const results = await Promise.all([
  tools.exec_command({cmd: "pwd", workdir: "/repo"}),
  tools.exec_command({cmd: ` + "`rg -n \"^\" ${file}`" + `, workdir: "/repo"}),
  tools.exec_command({cmd, workdir: "/repo"})
]); results.forEach(result => text(result.output));`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:  "exec",
			Input: map[string]any{"input": script},
			CWD:   "/session",
		})

		Expect(use.Tool).To(Equal("Bash"))
		Expect(use.CWD).To(Equal("/repo"))
		Expect(use.Input).To(Equal(map[string]any{
			"command": "pwd\nrg -n \"^\" {{js:file}}\n{{js:cmd}}",
			"input":   script,
		}))
	})

	It("renders per-command working directories when a composed exec spans directories", func() {
		script := `const results = await Promise.all([
  tools.exec_command({cmd: "pwd", workdir: "/repo/one"}),
  tools.exec_command({cmd: "ls", workdir: "/repo/two words"})
]);`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:  "exec",
			Input: map[string]any{"input": script},
			CWD:   "/session",
		})

		Expect(use.Tool).To(Equal("Bash"))
		Expect(use.CWD).To(Equal("/session"))
		Expect(use.Input["command"]).To(Equal(strings.Join([]string{
			"(",
			"  cd /repo/one",
			"  pwd",
			")",
			"(",
			"  cd '/repo/two words'",
			"  ls",
			")",
		}, "\n")))
	})

	It("uses placeholders for dynamic command arguments and working directories", func() {
		script := `const r = await tools.exec_command(options); text(r.output);`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:  "exec",
			Input: map[string]any{"input": script},
			CWD:   "/session",
		})

		Expect(use.Tool).To(Equal("Bash"))
		Expect(use.CWD).To(Equal("/session"))
		Expect(use.Input["command"]).To(Equal(strings.Join([]string{
			"(",
			"  cd {{js:options.workdir}}",
			"  {{js:options.cmd}}",
			")",
		}, "\n")))
	})

	It("does not treat exec_command text in strings or comments as a shell call", func() {
		script := `const example = "tools.exec_command({cmd: 'false'})";
// tools.exec_command({cmd: "also false"})
text(example);`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:  "exec",
			Input: map[string]any{"input": script},
		})

		Expect(use.Tool).To(Equal("exec"))
		Expect(use.Input).To(Equal(map[string]any{"input": script}))
	})

	It("preserves malformed exec scripts and exposes their parse error", func() {
		script := `const r = await tools.exec_command({`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:  "exec",
			Input: map[string]any{"input": script},
		})

		Expect(use.Tool).To(Equal("exec"))
		Expect(use.Input).To(And(
			HaveKeyWithValue("input", script),
			HaveKey("parse_error"),
		))
	})

	It("preserves named tool arguments without classifying them as shell commands", func() {
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:      "search",
			Namespace: "catalog",
			Arguments: []byte(`{"query":"captain"}`),
			ID:        "tool-1",
		})

		Expect(use).To(MatchFields(IgnoreExtras, Fields{
			"Tool":      Equal("search"),
			"Input":     Equal(map[string]any{"query": "captain"}),
			"Namespace": Equal("catalog"),
			"ToolUseID": Equal("tool-1"),
		}))
	})

	It("preserves scalar arguments for named tools", func() {
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:      "search",
			Arguments: []byte(`"catalog"`),
		})

		Expect(use.Input).To(Equal(map[string]any{"arguments": "catalog"}))
	})

	It("extracts current custom tool calls from rollout history", func() {
		stream := strings.Join([]string{
			`{"timestamp":"2026-07-16T10:00:00Z","type":"session_meta","payload":{"id":"session-1","cwd":"/repo"}}`,
			`{"timestamp":"2026-07-16T10:00:01Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"call-patch","input":"const patch = \"*** Begin Patch\\n*** Update File: app.go\\n*** End Patch\"; tools.apply_patch(patch);"}}`,
			`{"timestamp":"2026-07-16T10:00:02Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call-patch","output":"Success"}}`,
		}, "\n")

		uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(stream))
		Expect(err).ToNot(HaveOccurred())
		Expect(uses).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
			"Tool":       Equal("exec"),
			"Input":      HaveKeyWithValue("input", ContainSubstring("*** Update File: app.go")),
			"SessionID":  Equal("session-1"),
			"ToolUseID":  Equal("call-patch"),
			"RecordType": Equal("response_item.custom_tool_call"),
		})))
	})

	It("extracts Codex exec wrappers from rollout history as correlated Bash calls", func() {
		script := `const r = await tools.exec_command({"cmd":"rg -n TODO pkg","workdir":"/repo"}); text(r.output);`
		stream := strings.Join([]string{
			`{"timestamp":"2026-07-16T10:00:00Z","type":"session_meta","payload":{"id":"session-1","cwd":"/session"}}`,
			`{"timestamp":"2026-07-16T10:00:01Z","type":"response_item","payload":{"type":"custom_tool_call","name":"exec","call_id":"call-exec","input":` + string(mustJSON(script)) + `}}`,
			`{"timestamp":"2026-07-16T10:00:02Z","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call-exec","output":"Script completed\nOutput:\npkg/a.go:10:// TODO"}}`,
		}, "\n")

		uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(stream))
		Expect(err).ToNot(HaveOccurred())
		Expect(uses).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
			"Tool": Equal("Bash"),
			"Input": Equal(map[string]any{
				"command": "rg -n TODO pkg",
				"input":   script,
			}),
			"CWD":        Equal("/repo"),
			"SessionID":  Equal("session-1"),
			"ToolUseID":  Equal("call-exec"),
			"Response":   Equal("pkg/a.go:10:// TODO"),
			"RecordType": Equal("response_item.custom_tool_call"),
		})))
	})

	DescribeTable("normalizes nested error envelopes",
		func(input, expected string) {
			Expect(NormalizeCodexError(input)).To(Equal(expected))
		},
		Entry("plain", "boom", "boom"),
		Entry("empty", "", ""),
		Entry("nested error", `{"type":"error","status":400,"error":{"type":"invalid_request_error","message":"bad model"}}`, "bad model"),
		Entry("top-level message", `{"message":"top"}`, "top"),
		Entry("non-json passthrough", "not { json", "not { json"),
	)
})

func mustJSON(value string) []byte {
	data, err := json.Marshal(value)
	Expect(err).ToNot(HaveOccurred())
	return data
}
