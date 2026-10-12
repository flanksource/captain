package genkit

import (
	"context"
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Genkit turn events", func() {
	const lookupThenTruncated = `anthropic:
  - respond:
      tool_use: {name: lookup, id: toolu_lookup, input: {key: alpha}}
      usage: {input: 20, output: 5}
  - match: {tool_result_for: lookup}
    respond:
      text: '{"status":'
      stop_reason: max_tokens
      usage: {input: 30, output: 7}
`
	statusSchema := json.RawMessage(`{"type":"object","properties":{"status":{"type":"string"}},"required":["status"]}`)

	lookupTool := api.ToolDefinition{
		Name: "lookup", DefaultPermission: api.ToolPolicyAllow,
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}},
		Handler: func(context.Context, map[string]any) (any, error) {
			return map[string]any{"value": "found"}, nil
		},
	}

	It("emits every message of a tool loop, and the usage of a turn whose output then fails validation", func(ctx SpecContext) {
		provider, _ := newBufferedAnthropicProvider(lookupThenTruncated)
		provider.cfg.Tools = []api.ToolDefinition{lookupTool}

		events, err := provider.ExecuteStream(ctx, bufferedAnthropicRequest(api.Prompt{
			System: "Look things up.", User: "Find alpha.", SchemaJSON: statusSchema,
		}))
		Expect(err).NotTo(HaveOccurred())

		var turns []ai.Event
		var final ai.Event
		for event := range events {
			switch event.Kind {
			case ai.EventTurn:
				turns = append(turns, event)
			case ai.EventResult, ai.EventError:
				final = event
			}
		}

		Expect(turns).NotTo(BeEmpty())
		Expect(turns[0].Message.Role).To(Equal(api.RoleSystem))
		Expect(turns[0].Message.Parts).To(HaveExactElements(
			api.Part{Type: api.PartText, Text: "Look things up."},
			HaveField("Text", ContainSubstring("Output should be in JSON format")),
		), "the transcript records the schema instruction genkit sends with the system prompt")
		Expect(turns[1:]).To(HaveExactElements(
			turnOf(api.RoleUser, nil, api.Part{Type: api.PartText, Text: "Find alpha."}),
			turnOf(api.RoleAssistant, &ai.Usage{InputTokens: 20, OutputTokens: 5}, api.Part{
				Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{ToolCallID: "toolu_lookup", Name: "lookup", Input: json.RawMessage(`{"key":"alpha"}`)},
			}),
			turnOf(api.RoleTool, nil, api.Part{
				Type: api.PartToolResult, ToolResult: &api.ToolResult{ToolCallID: "toolu_lookup", Output: json.RawMessage(`{"value":"found"}`)},
			}),
			turnOf(api.RoleAssistant, &ai.Usage{InputTokens: 30, OutputTokens: 7}, api.Part{Type: api.PartText, Text: `{"status":`}),
		))
		Expect(final.Kind).To(Equal(ai.EventError))
		Expect(final.Error).To(ContainSubstring(ai.ErrOutputTruncated.Error()), "a reply cut off at max_tokens is not reported as invalid JSON")
		Expect(final.Error).To(ContainSubstring("max_tokens"))
		Expect(final.Usage).To(Equal(&ai.Usage{InputTokens: 50, OutputTokens: 12}))
		Expect(final.CostUSD).To(BeNumerically(">", 0))
	})

	It("reports a complete answer that fails the schema as a schema error carrying the rejected answer", func(ctx SpecContext) {
		provider, _ := newBufferedAnthropicProvider(`anthropic:
  - respond:
      text: '{"status":7}'
      usage: {input: 30, output: 7}
`)
		events, err := provider.ExecuteStream(ctx, bufferedAnthropicRequest(api.Prompt{User: "Find alpha.", SchemaJSON: statusSchema}))
		Expect(err).NotTo(HaveOccurred())
		var final ai.Event
		for event := range events {
			if event.Kind == ai.EventError || event.Kind == ai.EventResult {
				final = event
			}
		}
		Expect(final.Kind).To(Equal(ai.EventError))
		Expect(final.Error).To(ContainSubstring(ai.ErrSchemaValidation.Error()))
		Expect(final.Text).To(Equal(`{"status":7}`), "the repair turn is shown what was rejected")
		Expect(final.Usage).To(Equal(&ai.Usage{InputTokens: 30, OutputTokens: 7}))
	})

	It("reports the usage of every turn on the result, not just the last", func(ctx SpecContext) {
		provider, _ := newBufferedAnthropicProvider(`anthropic:
  - respond:
      tool_use: {name: lookup, id: toolu_lookup, input: {key: alpha}}
      usage: {input: 20, output: 5}
  - match: {tool_result_for: lookup}
    respond:
      text: Found it.
      usage: {input: 30, output: 7}
`)
		provider.cfg.Tools = []api.ToolDefinition{lookupTool}

		streamed, err := provider.ExecuteStream(ctx, bufferedAnthropicRequest(api.Prompt{User: "Find alpha."}))
		Expect(err).NotTo(HaveOccurred())
		var result ai.Event
		for event := range streamed {
			if event.Kind == ai.EventResult {
				result = event
			}
		}
		Expect(result.Usage).To(Equal(&ai.Usage{InputTokens: 50, OutputTokens: 12}))

		provider, _ = newBufferedAnthropicProvider(`anthropic:
  - respond:
      tool_use: {name: lookup, id: toolu_lookup, input: {key: alpha}}
      usage: {input: 20, output: 5}
  - match: {tool_result_for: lookup}
    respond:
      text: Found it.
      usage: {input: 30, output: 7}
`)
		provider.cfg.Tools = []api.ToolDefinition{lookupTool}
		buffered, err := provider.Execute(ctx, bufferedAnthropicRequest(api.Prompt{User: "Find alpha."}))
		Expect(err).NotTo(HaveOccurred())
		Expect(buffered.Usage).To(Equal(ai.Usage{InputTokens: 50, OutputTokens: 12}))
		Expect(buffered.CostUSD).To(BeNumerically("~", provider.costUSD(ai.Usage{InputTokens: 50, OutputTokens: 12}), 1e-12))
	})
})

func turnOf(role api.MessageRole, usage *ai.Usage, parts ...api.Part) ai.Event {
	return ai.Event{
		Kind: ai.EventTurn, Model: bufferedAnthropicModel, Usage: usage,
		Message: &api.Message{Role: role, Parts: parts},
	}
}
