package openai

import (
	"context"
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/aimock"
	"github.com/flanksource/captain/pkg/aimock/openaimock"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("OpenAI turn events", func() {
	It("emits every message of a tool loop, and keeps the usage of a run whose final output is invalid", func(ctx SpecContext) {
		scenario, err := aimock.Parse([]byte(`openai:
  - respond:
      function_call: {name: lookup, call_id: call_lookup, arguments: {key: alpha}}
      usage: {input: 20, output: 5}
  - match: {tool_result_for: lookup}
    respond:
      text: '{"status":'
      usage: {input: 30, output: 7}
`))
		Expect(err).NotTo(HaveOccurred())
		server, err := openaimock.Start(openaimock.Options{Scenario: scenario})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(server.Close)
		provider, err := New(ai.Config{
			Model:  api.Model{Name: "gpt-5", Provider: api.OpenAI, Mode: api.ModeAPI},
			APIKey: aimock.DummyKey, APIURL: server.APIURL(),
			Tools: []api.ToolDefinition{{
				Name: "lookup", DefaultPermission: api.ToolPolicyAllow,
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{"key": map[string]any{"type": "string"}}},
				Handler: func(context.Context, map[string]any) (any, error) {
					return map[string]any{"value": "found"}, nil
				},
			}},
		})
		Expect(err).NotTo(HaveOccurred())

		events, err := provider.ExecuteStream(ctx, ai.Request{Prompt: api.Prompt{
			System: "Look things up.", User: "Find alpha.",
			SchemaJSON: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string"}},"required":["status"],"additionalProperties":false}`),
		}})
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

		turn := func(role api.MessageRole, usage *ai.Usage, parts ...api.Part) ai.Event {
			return ai.Event{Kind: ai.EventTurn, Model: "gpt-5", Usage: usage, Message: &api.Message{Role: role, Parts: parts}}
		}
		Expect(turns).To(HaveExactElements(
			turn(api.RoleSystem, nil, api.Part{Type: api.PartText, Text: "Look things up."}),
			turn(api.RoleUser, nil, api.Part{Type: api.PartText, Text: "Find alpha."}),
			turn(api.RoleAssistant, &ai.Usage{InputTokens: 20, OutputTokens: 5}, api.Part{
				Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{ToolCallID: "call_lookup", Name: "lookup", Input: json.RawMessage(`{"key":"alpha"}`)},
			}),
			turn(api.RoleTool, nil, api.Part{
				Type: api.PartToolResult, ToolResult: &api.ToolResult{ToolCallID: "call_lookup", Output: json.RawMessage(`{"value":"found"}`)},
			}),
			turn(api.RoleAssistant, &ai.Usage{InputTokens: 30, OutputTokens: 7}, api.Part{Type: api.PartText, Text: `{"status":`}),
		))
		Expect(final.Kind).To(Equal(ai.EventError))
		Expect(final.Error).To(ContainSubstring(ai.ErrSchemaValidation.Error()))
		Expect(final.Usage).To(Equal(&ai.Usage{InputTokens: 50, OutputTokens: 12}))
		Expect(final.CostUSD).To(BeNumerically(">", 0))
	})

	It("sends a structured tool result's content to the model and its data to the event", func(ctx SpecContext) {
		const preview = "| id |\n|----|\n| j-1 |"
		scenario, err := aimock.Parse([]byte(`openai:
  - respond:
      function_call: {name: journals, call_id: call_journals, arguments: {}}
  - match: {tool_result_for: journals}
    respond:
      text: done
`))
		Expect(err).NotTo(HaveOccurred())
		server, err := openaimock.Start(openaimock.Options{Scenario: scenario})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(server.Close)
		provider, err := New(ai.Config{
			Model:  api.Model{Name: "gpt-5", Provider: api.OpenAI, Mode: api.ModeAPI},
			APIKey: aimock.DummyKey, APIURL: server.APIURL(),
			Tools: []api.ToolDefinition{{
				Name: "journals", DefaultPermission: api.ToolPolicyAllow,
				Handler: func(context.Context, map[string]any) (any, error) {
					return api.StructuredToolResult{Content: preview, Structured: map[string]any{"data": []any{map[string]any{"id": "j-1"}}}}, nil
				},
			}},
		})
		Expect(err).NotTo(HaveOccurred())

		events, err := provider.ExecuteStream(ctx, ai.Request{Prompt: api.Prompt{User: "List journals."}})
		Expect(err).NotTo(HaveOccurred())
		var result ai.Event
		var history *api.ToolResult
		for event := range events {
			if event.Kind == ai.EventToolResult {
				result = event
			}
			if event.Kind == ai.EventTurn && event.Message.Role == api.RoleTool {
				history = event.Message.Parts[0].ToolResult
			}
		}

		Expect(result.Success).To(BeTrue())
		Expect(result.Text).To(Equal(preview))
		Expect(result.Output).To(MatchJSON(`{"data":[{"id":"j-1"}]}`))
		Expect(history.Output).To(MatchJSON(`"| id |\n|----|\n| j-1 |"`))
		requests := server.Requests()
		Expect(requests).To(HaveLen(2))
		modelSaw := requests[1].Request.Messages[len(requests[1].Request.Messages)-1].Content
		Expect(modelSaw).To(ContainSubstring("| j-1 |"))
		Expect(modelSaw).NotTo(ContainSubstring(`"data"`))
	})
})
