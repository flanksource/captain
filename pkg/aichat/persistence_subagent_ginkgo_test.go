package aichat

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("assistant message builder with subagent tools", func() {
	It("persists subagent tools under their parent and closes the ones the turn outlived", func() {
		builder, err := newAssistantMessageBuilder(assistantMessageBuilderOptions{MessageID: "m-1"})
		Expect(err).NotTo(HaveOccurred())
		for _, event := range []api.Event{
			{Kind: api.EventToolUse, ToolCallID: "agent-1", Tool: "Agent"},
			{Kind: api.EventToolResult, ToolCallID: "agent-1", Tool: "Agent", Text: "launched", Success: true},
			{Kind: api.EventToolUse, ToolCallID: "sub-1", Tool: "Bash", ParentToolCallID: "agent-1"},
			{Kind: api.EventToolResult, ToolCallID: "sub-1", Text: `"ok"`, Success: true, ParentToolCallID: "agent-1"},
			{Kind: api.EventToolUse, ToolCallID: "sub-2", Tool: "Bash", ParentToolCallID: "agent-1"},
			{Kind: api.EventResult, Success: true},
		} {
			Expect(builder.apply(event)).To(Succeed())
		}

		type toolView struct {
			ID, State, Parent, Error string
		}
		var tools []toolView
		for _, part := range builder.message.Parts {
			if !part.IsTool() {
				continue
			}
			view := toolView{ID: part.ToolCallID, State: part.State, Error: part.ErrorText}
			if part.ToolMetadata != nil {
				view.Parent = part.ToolMetadata.ParentToolCallID
			}
			tools = append(tools, view)
		}
		Expect(tools).To(Equal([]toolView{
			{ID: "agent-1", State: "output-available"},
			{ID: "sub-1", State: "output-available", Parent: "agent-1"},
			{ID: "sub-2", State: "output-error", Parent: "agent-1", Error: "subagent did not finish before the turn ended"},
		}))
	})

	It("still rejects a top-level tool left without a result", func() {
		builder, err := newAssistantMessageBuilder(assistantMessageBuilderOptions{MessageID: "m-1"})
		Expect(err).NotTo(HaveOccurred())
		Expect(builder.apply(api.Event{Kind: api.EventToolUse, ToolCallID: "call-1", Tool: "Bash"})).To(Succeed())
		Expect(builder.apply(api.Event{Kind: api.EventResult, Success: true})).
			To(MatchError(`persist tool call "call-1" ended in non-terminal state "input-available"`))
	})
})
