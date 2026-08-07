package genkit

import (
	"context"
	"encoding/json"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
	gk "github.com/firebase/genkit/go/genkit"
)

var _ = Describe("Genkit tool event correlation", func() {
	var (
		provider *Provider
		events   []ai.Event
		emit     func(ai.Event)
		tool     api.ToolDefinition
	)

	BeforeEach(func() {
		provider = newToolProvider(nil)
		events = nil
		emit = func(event ai.Event) { events = append(events, event) }
		tool = api.ToolDefinition{
			Name:              "lookup",
			DefaultPermission: api.ToolModeOn,
			Handler: func(_ context.Context, input map[string]any) (any, error) {
				return map[string]any{"city": input["city"]}, nil
			},
		}
	})

	It("uses the Anthropic provider reference for one lifecycle", func() {
		correlation := newToolEventCorrelation()
		request := &gkai.ToolRequest{Name: tool.Name, Ref: "toolu_123", Input: map[string]any{"city": "Cape Town"}}

		mapped, err := chunkToEvents(toolRequestChunk(request), provider.GetModel(), correlation)
		Expect(err).NotTo(HaveOccurred())
		Expect(mapped).To(BeEmpty())
		mapped, err = chunkToEvents(toolRequestChunk(request), provider.GetModel(), correlation)
		Expect(err).NotTo(HaveOccurred())
		Expect(mapped).To(BeEmpty())

		_, err = provider.runTool(context.WithValue(context.Background(), genkitToolRequestContextKey{}, request), tool, request.Input, emit, correlation)
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(2))
		Expect(events[0].Kind).To(Equal(ai.EventToolUse))
		Expect(events[0].ToolCallID).To(Equal(request.Ref))
		Expect(events[1].Kind).To(Equal(ai.EventToolResult))
		Expect(events[1].ToolCallID).To(Equal(request.Ref))

		mapped, err = chunkToEvents(toolResponseChunk(tool.Name, request.Ref), provider.GetModel(), correlation)
		Expect(err).NotTo(HaveOccurred())
		Expect(mapped).To(BeEmpty())
	})

	It("ignores OpenAI argument deltas after retaining the provider reference", func() {
		correlation := newToolEventCorrelation()
		first := &gkai.ToolRequest{Name: tool.Name, Ref: "call_123", Input: `{"city"`}
		delta := &gkai.ToolRequest{Input: `:"Cape Town"}`}

		_, err := chunkToEvents(toolRequestChunk(first), provider.GetModel(), correlation)
		Expect(err).NotTo(HaveOccurred())
		_, err = chunkToEvents(toolRequestChunk(delta), provider.GetModel(), correlation)
		Expect(err).NotTo(HaveOccurred())

		_, err = provider.runTool(context.WithValue(context.Background(), genkitToolRequestContextKey{}, first), tool, map[string]any{"city": "Cape Town"}, emit, correlation)
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(2))
		Expect(events[0].ToolCallID).To(Equal(first.Ref))
		Expect(events[1].ToolCallID).To(Equal(first.Ref))
	})

	It("preserves a Gemini reference assigned after its chunk was observed", func() {
		correlation := newToolEventCorrelation()
		request := &gkai.ToolRequest{Name: tool.Name, Input: map[string]any{"city": "Cape Town"}}

		_, err := chunkToEvents(toolRequestChunk(request), provider.GetModel(), correlation)
		Expect(err).NotTo(HaveOccurred())
		request.Ref = "genkit-assigned-ref"

		_, err = provider.runTool(context.WithValue(context.Background(), genkitToolRequestContextKey{}, request), tool, request.Input, emit, correlation)
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(2))
		Expect(events[0].ToolCallID).To(Equal(request.Ref))
		Expect(events[1].ToolCallID).To(Equal(request.Ref))
	})

	It("keeps permission events on the provider reference", func() {
		provider = newToolProvider(func(_ context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
			Expect(request.ToolUseID).To(Equal("toolu_approved"))
			return api.PermissionDecision{Allow: true}, nil
		})
		tool.DefaultPermission = api.ToolModeAsk
		correlation := newToolEventCorrelation()
		request := &gkai.ToolRequest{Name: tool.Name, Ref: "toolu_approved", Input: map[string]any{"city": "Cape Town"}}
		_, err := chunkToEvents(toolRequestChunk(request), provider.GetModel(), correlation)
		Expect(err).NotTo(HaveOccurred())

		_, err = provider.runTool(context.WithValue(context.Background(), genkitToolRequestContextKey{}, request), tool, request.Input, emit, correlation)
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(3))
		Expect(events[1].Kind).To(Equal(ai.EventPermission))
		Expect(events[1].ToolCallID).To(Equal(request.Ref))
	})

	It("correlates concurrent same-name calls after schema normalization", func(ctx SpecContext) {
		var runs atomic.Int32
		genkit := gk.Init(ctx)
		modelRef := "test/parallel-journals"
		gk.DefineModel(genkit, modelRef, &gkai.ModelOptions{Supports: &gkai.ModelSupports{Tools: true, Multiturn: true}},
			func(_ context.Context, request *gkai.ModelRequest, stream gkai.ModelStreamCallback) (*gkai.ModelResponse, error) {
				if request.Messages[len(request.Messages)-1].Role == gkai.RoleTool {
					return &gkai.ModelResponse{Message: gkai.NewModelTextMessage("done"), FinishReason: gkai.FinishReasonStop}, nil
				}
				message := gkai.NewModelMessage(
					gkai.NewToolRequestPart(&gkai.ToolRequest{Name: "journals", Ref: "call-10", Input: json.RawMessage(`{"limit":10}`)}),
					gkai.NewToolRequestPart(&gkai.ToolRequest{Name: "journals", Ref: "call-20", Input: json.RawMessage(`{"limit":20}`)}),
				)
				Expect(stream(ctx, &gkai.ModelResponseChunk{Role: gkai.RoleModel, Content: message.Content})).To(Succeed())
				return &gkai.ModelResponse{Message: message, FinishReason: gkai.FinishReasonStop}, nil
			})

		provider := &Provider{
			cfg: ai.Config{
				Model: api.Model{Name: "parallel-journals", Backend: api.BackendAnthropic},
				Tools: []api.ToolDefinition{{
					Name:              "journals",
					DefaultPermission: api.ToolModeOn,
					InputSchema: map[string]any{
						"type":       "object",
						"properties": map[string]any{"limit": map[string]any{"type": "integer"}},
						"required":   []string{"limit"},
					},
					Handler: func(_ context.Context, input map[string]any) (any, error) {
						Expect(input["limit"]).To(BeAssignableToTypeOf(int64(0)))
						runs.Add(1)
						return input, nil
					},
				}},
			},
			backend: api.BackendAnthropic,
			g:       genkit, modelRef: modelRef,
		}

		stream, err := provider.ExecuteStream(ctx, api.Spec{Prompt: api.Prompt{User: "Inspect both journal sets."}})
		Expect(err).NotTo(HaveOccurred())
		var callIDs []string
		for event := range stream {
			Expect(event.Kind).NotTo(Equal(ai.EventError), event.Error)
			if event.Kind == ai.EventToolUse {
				callIDs = append(callIDs, event.ToolCallID)
			}
		}
		Expect(runs.Load()).To(Equal(int32(2)))
		Expect(callIDs).To(ConsistOf("call-10", "call-20"))
	})

	It("fails loudly when a tool response has no correlated request", func() {
		correlation := newToolEventCorrelation()
		_, err := chunkToEvents(toolResponseChunk(tool.Name, "missing"), provider.GetModel(), correlation)
		Expect(err).To(MatchError(ContainSubstring(`uncorrelated tool response "missing"`)))
	})

	It("fails loudly when execution has no provider request", func() {
		correlation := newToolEventCorrelation()
		_, err := provider.runTool(context.Background(), tool, map[string]any{"city": "Cape Town"}, emit, correlation)
		Expect(err).To(MatchError(ContainSubstring(`no provider request context`)))
		Expect(events).To(BeEmpty())
	})
})

func toolRequestChunk(request *gkai.ToolRequest) *gkai.ModelResponseChunk {
	return &gkai.ModelResponseChunk{Role: gkai.RoleModel, Content: []*gkai.Part{gkai.NewToolRequestPart(request)}}
}

func toolResponseChunk(name, ref string) *gkai.ModelResponseChunk {
	return &gkai.ModelResponseChunk{Role: gkai.RoleTool, Content: []*gkai.Part{gkai.NewToolResponsePart(&gkai.ToolResponse{Name: name, Ref: ref, Output: "ok"})}}
}
