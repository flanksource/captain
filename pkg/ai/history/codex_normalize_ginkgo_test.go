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

	It("combines composed exec commands in source order", func() {
		script := `const results = await Promise.all([
  tools.exec_command({cmd: "pwd", workdir: "/repo"}),
  tools.exec_command({cmd: "ls -la", workdir: "/repo"})
]); results.forEach(result => text(result.output));`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:  "exec",
			Input: map[string]any{"input": script},
			CWD:   "/session",
		})

		Expect(use.Tool).To(Equal("Bash"))
		Expect(use.CWD).To(Equal("/repo"))
		Expect(use.Input).To(Equal(map[string]any{
			"command": "pwd\nls -la",
			"input":   script,
		}))
	})

	It("resolves commands bound to script-scope constants and unrolled iteration", func() {
		script := `const jobs = [["status", "git status --short"], ["log", "git log -1"]];
const results = await Promise.all(jobs.map(async ([name, cmd]) => {
  const r = await tools.exec_command({cmd, workdir: "/repo"});
  return {name, output: r.output};
}));`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:  "exec",
			Input: map[string]any{"input": script},
			CWD:   "/session",
		})

		Expect(use.Tool).To(Equal("Bash"))
		Expect(use.Input["command"]).To(Equal("git status --short\ngit log -1"))
	})

	It("never renders an unresolved JavaScript expression as a shell command", func() {
		script := `const results = await Promise.all([
  tools.exec_command({cmd: "pwd", workdir: "/repo"}),
  tools.exec_command({cmd, workdir: "/repo"})
]);`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:  "exec",
			Input: map[string]any{"input": script},
			CWD:   "/session",
		})

		// The row shows the script it could not resolve rather than a mustache
		// masquerading as a command.
		Expect(use.Tool).To(Equal("CodexExecScript"))
		Expect(use.Input).To(And(
			HaveKeyWithValue("script", script),
			HaveKey("parse_error"),
		))
		Expect(use.Input).ToNot(HaveKey("command"))
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

	It("reports an unresolvable options object instead of inventing a command", func() {
		script := `const r = await tools.exec_command(options); text(r.output);`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:  "exec",
			Input: map[string]any{"input": script},
			CWD:   "/session",
		})

		Expect(use.Tool).To(Equal("CodexExecScript"))
		Expect(use.CWD).To(Equal("/session"))
		Expect(use.Input).To(HaveKey("parse_error"))
	})

	It("does not treat exec_command text in strings or comments as a shell call", func() {
		script := `const example = "tools.exec_command({cmd: 'false'})";
// tools.exec_command({cmd: "also false"})
text(example);`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:  "exec",
			Input: map[string]any{"input": script},
		})

		// A script that only prints parses cleanly and invokes nothing: it gets a
		// row showing the script, not a shell row and not a parse error.
		Expect(use.Tool).To(Equal("CodexExecScript"))
		Expect(use.Input).To(Equal(map[string]any{"script": script}))
	})

	It("preserves malformed exec scripts and exposes their parse error", func() {
		script := `const r = await tools.exec_command({`
		use := NormalizeCodexToolCall(CodexToolCall{
			Name:  "exec",
			Input: map[string]any{"input": script},
		})

		Expect(use.Tool).To(Equal("CodexExecScript"))
		Expect(use.Input).To(And(
			HaveKeyWithValue("script", script),
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
		// A single-file patch, even applied from inside a freeform script, is the
		// same file edit Claude records — not an opaque patch payload.
		Expect(uses).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
			"Tool":       Equal("Edit"),
			"Input":      HaveKeyWithValue("file_path", "app.go"),
			"SessionID":  Equal("session-1"),
			"ToolUseID":  Equal("call-patch"),
			"RecordType": Equal("response_item.custom_tool_call"),
			"SourceLine": Equal(int64(2)),
		})))
	})

	It("splits a freeform script's shell commands and file writes into separate rows", func() {
		script := `await tools.exec_command({cmd: "go build ./...", workdir: "/repo"});
tools.apply_patch("*** Begin Patch\n*** Add File: pkg/new.go\n+package pkg\n*** End Patch");`
		use := CodexToolCall{
			Name:      "exec",
			Input:     map[string]any{"input": script},
			CWD:       "/repo",
			ID:        "call-mixed",
			SessionID: "session-1",
			Response:  "ok",
		}

		uses := NormalizeCodexToolCalls(use)
		Expect(uses).To(HaveLen(2))
		Expect(uses[0].Tool).To(Equal("Bash"))
		Expect(uses[0].Input["command"]).To(Equal("go build ./..."))
		Expect(uses[0].ToolUseID).To(Equal("call-mixed"))
		// The output belongs to the shell row; the second row is a distinct
		// operation and must not claim the same tool-use id.
		Expect(uses[0].Response).To(Equal("ok"))
		Expect(uses[1].Tool).To(Equal("Write"))
		Expect(uses[1].Input["file_path"]).To(Equal("pkg/new.go"))
		Expect(uses[1].Input["content"]).To(Equal("package pkg"))
		Expect(uses[1].ToolUseID).To(Equal("call-mixed#1"))
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
