package history

import (
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
