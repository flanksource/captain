package aichat

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("tool output display values", func() {
	const preview = "| id |\n| j-1 |"
	display := json.RawMessage(`{"data":[{"id":"j-1"}],"page":{"limit":25}}`)
	events := []api.Event{
		{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "journals"},
		{Kind: api.EventToolResult, ToolCallID: "call-1", Tool: "journals", Text: preview, Output: display, Success: true},
		{Kind: api.EventResult, Success: true},
	}

	It("persists the display value so a replayed session renders the same data", func() {
		builder, err := newAssistantMessageBuilder(assistantMessageBuilderOptions{MessageID: "m-1"})
		Expect(err).NotTo(HaveOccurred())
		for _, event := range events {
			Expect(builder.apply(event)).To(Succeed())
		}
		var output json.RawMessage
		for _, part := range builder.message.Parts {
			if part.IsTool() {
				output = part.Output
			}
		}
		Expect(output).To(MatchJSON(display))
	})

	It("keeps the model text as the output when there is no separate display value", func() {
		builder, err := newAssistantMessageBuilder(assistantMessageBuilderOptions{MessageID: "m-1"})
		Expect(err).NotTo(HaveOccurred())
		plain := []api.Event{events[0], {Kind: api.EventToolResult, ToolCallID: "call-1", Tool: "journals", Text: preview, Success: true}, events[2]}
		for _, event := range plain {
			Expect(builder.apply(event)).To(Succeed())
		}
		for _, part := range builder.message.Parts {
			if part.IsTool() {
				Expect(part.Output).To(MatchJSON(`"| id |\n| j-1 |"`))
			}
		}
	})

	It("rejects a display value that is not JSON instead of storing a broken part", func() {
		builder, err := newAssistantMessageBuilder(assistantMessageBuilderOptions{MessageID: "m-1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(builder.apply(events[0])).To(Succeed())
		broken := events[1]
		broken.Output = json.RawMessage(`{"data":`)
		Expect(builder.apply(broken)).To(MatchError(ContainSubstring("journals")))
	})
})
