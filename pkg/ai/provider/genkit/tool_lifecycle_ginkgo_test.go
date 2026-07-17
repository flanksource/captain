package genkit

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
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

		_, err = provider.runTool(context.Background(), tool, request.Input, emit, correlation)
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

		_, err = provider.runTool(context.Background(), tool, map[string]any{"city": "Cape Town"}, emit, correlation)
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

		_, err = provider.runTool(context.Background(), tool, request.Input, emit, correlation)
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

		_, err = provider.runTool(context.Background(), tool, request.Input, emit, correlation)
		Expect(err).NotTo(HaveOccurred())
		Expect(events).To(HaveLen(3))
		Expect(events[1].Kind).To(Equal(ai.EventPermission))
		Expect(events[1].ToolCallID).To(Equal(request.Ref))
	})

	It("fails loudly when a tool response has no correlated request", func() {
		correlation := newToolEventCorrelation()
		_, err := chunkToEvents(toolResponseChunk(tool.Name, "missing"), provider.GetModel(), correlation)
		Expect(err).To(MatchError(ContainSubstring(`uncorrelated tool response "missing"`)))
	})

	It("fails loudly when execution has no provider request", func() {
		correlation := newToolEventCorrelation()
		_, err := provider.runTool(context.Background(), tool, map[string]any{"city": "Cape Town"}, emit, correlation)
		Expect(err).To(MatchError(ContainSubstring(`no correlated provider request`)))
		Expect(events).To(BeEmpty())
	})
})

func toolRequestChunk(request *gkai.ToolRequest) *gkai.ModelResponseChunk {
	return &gkai.ModelResponseChunk{Role: gkai.RoleModel, Content: []*gkai.Part{gkai.NewToolRequestPart(request)}}
}

func toolResponseChunk(name, ref string) *gkai.ModelResponseChunk {
	return &gkai.ModelResponseChunk{Role: gkai.RoleTool, Content: []*gkai.Part{gkai.NewToolResponsePart(&gkai.ToolResponse{Name: name, Ref: ref, Output: "ok"})}}
}
