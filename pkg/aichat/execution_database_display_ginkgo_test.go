package aichat

import (
	"context"
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"
)

var _ = Describe("caller-tool display correlation", func() {
	var execution *databaseExecution
	display := json.RawMessage(`{"data":[{"id":"j-1"}]}`)
	input := map[string]any{"limit": "25"}

	observe := func(event api.Event) api.Event {
		observed, err := execution.Observe(context.Background(), event)
		Expect(err).NotTo(HaveOccurred())
		return observed
	}
	result := func(callID string) api.Event {
		return api.Event{Kind: api.EventToolResult, ToolCallID: callID, Tool: "journals", Text: "| id |", Success: true}
	}

	BeforeEach(func() {
		execution = &databaseExecution{}
	})

	It("attaches the display value to the provider result with the same tool-use ID", func() {
		observe(api.Event{Kind: api.EventToolUse, ToolCallID: "toolu_1", Tool: "journals", Input: input})
		execution.rememberCallerDisplay(callertools.CallResult{Tool: "journals", Input: input, ToolUseID: "toolu_1", Output: display})

		Expect(observe(result("toolu_1")).Output).To(MatchJSON(display))
	})

	It("matches a generated tool-use ID by tool and input, as approval correlation does", func() {
		observe(api.Event{Kind: api.EventToolUse, ToolCallID: "toolu_2", Tool: "journals", Input: input})
		execution.rememberCallerDisplay(callertools.CallResult{Tool: "journals", Input: input, ToolUseID: "generated", ToolUseIDGenerated: true, Output: display})

		Expect(observe(result("toolu_2")).Output).To(MatchJSON(display))
	})

	It("leaves a result alone when no display value belongs to its call", func() {
		observe(api.Event{Kind: api.EventToolUse, ToolCallID: "toolu_3", Tool: "journals", Input: input})
		execution.rememberCallerDisplay(callertools.CallResult{Tool: "journals", Input: map[string]any{"limit": "50"}, ToolUseID: "generated", ToolUseIDGenerated: true, Output: display})

		Expect(observe(result("toolu_3")).Output).To(BeNil())
	})

	It("hands each display value to one result only", func() {
		observe(api.Event{Kind: api.EventToolUse, ToolCallID: "toolu_4", Tool: "journals", Input: input})
		execution.rememberCallerDisplay(callertools.CallResult{Tool: "journals", Input: input, ToolUseID: "toolu_4", Output: display})
		observe(result("toolu_4"))

		Expect(observe(result("toolu_4")).Output).To(BeNil())
	})
})
