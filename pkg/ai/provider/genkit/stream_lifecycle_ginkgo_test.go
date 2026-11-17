package genkit

import (
	"context"
	"sync"
	"time"

	gkai "github.com/firebase/genkit/go/ai"
	gk "github.com/firebase/genkit/go/genkit"
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Genkit stream lifecycle", func() {
	It("closes a failed generation before a delayed sibling tool returns", func(ctx SpecContext) {
		started, release, returned := make(chan struct{}), make(chan struct{}), make(chan struct{})
		releaseTool := sync.OnceFunc(func() { close(release) })
		DeferCleanup(releaseTool)
		genkit := gk.Init(ctx)
		modelRef := "test/failed-parallel-tools"
		gk.DefineModel(genkit, modelRef, &gkai.ModelOptions{Supports: &gkai.ModelSupports{Tools: true, Multiturn: true}},
			func(modelCtx context.Context, _ *gkai.ModelRequest, stream gkai.ModelStreamCallback) (*gkai.ModelResponse, error) {
				message := gkai.NewModelMessage(
					gkai.NewToolRequestPart(&gkai.ToolRequest{Name: "lookup", Ref: "slow", Input: map[string]any{"city": "Cape Town"}}),
					gkai.NewToolRequestPart(&gkai.ToolRequest{Name: "lookup", Ref: "invalid", Input: map[string]any{"city": 1}}),
				)
				if err := stream(modelCtx, &gkai.ModelResponseChunk{Role: gkai.RoleModel, Content: message.Content}); err != nil {
					return nil, err
				}
				return &gkai.ModelResponse{Message: message, FinishReason: gkai.FinishReasonStop}, nil
			})
		provider := &Provider{
			cfg: ai.Config{Model: api.Model{Name: "failed-parallel-tools", Mode: api.ModeAPI}, Tools: []api.ToolDefinition{{
				Name: "lookup", DefaultPermission: api.ToolPolicyAllow,
				InputSchema: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"}},
				Handler: func(toolCtx context.Context, _ map[string]any) (any, error) {
					defer close(returned)
					close(started)
					<-release
					return nil, toolCtx.Err()
				},
			}}},
			provider: ai.Anthropic, g: genkit, modelRef: modelRef,
		}
		stream, err := provider.ExecuteStream(ctx, api.Spec{Prompt: api.Prompt{User: "Look up both cities."}})
		Expect(err).NotTo(HaveOccurred())
		Eventually(started).Should(BeClosed())
		var terminal ai.Event
		for event := range stream {
			terminal = event
		}
		Expect(terminal.Kind).To(Equal(ai.EventError))
		Expect(terminal.Error).To(ContainSubstring("city"))
		Expect(returned).NotTo(BeClosed())
		releaseTool()
		Eventually(returned).Should(BeClosed())
	}, SpecTimeout(10*time.Second))

	It("rejects callbacks after the event stream closes", func(ctx SpecContext) {
		genkit := gk.Init(ctx)
		modelRef := "test/late-stream-callback"
		var callback gkai.ModelStreamCallback
		gk.DefineModel(genkit, modelRef, &gkai.ModelOptions{},
			func(_ context.Context, _ *gkai.ModelRequest, stream gkai.ModelStreamCallback) (*gkai.ModelResponse, error) {
				callback = stream
				return &gkai.ModelResponse{Message: gkai.NewModelTextMessage("complete"), FinishReason: gkai.FinishReasonStop}, nil
			})
		provider := &Provider{
			cfg:      ai.Config{Model: api.Model{Name: "late-stream-callback", Mode: api.ModeAPI}},
			provider: ai.Anthropic, g: genkit, modelRef: modelRef,
		}
		stream, err := provider.ExecuteStream(ctx, api.Spec{Prompt: api.Prompt{User: "Return a response."}})
		Expect(err).NotTo(HaveOccurred())
		var events []ai.Event
		for event := range stream {
			events = append(events, event)
		}
		Expect(events[len(events)-1].Kind).To(Equal(ai.EventResult))
		Expect(callback).NotTo(BeNil())
		var lateErr error
		Expect(func() {
			lateErr = callback(ctx, &gkai.ModelResponseChunk{Role: gkai.RoleModel, Content: []*gkai.Part{gkai.NewTextPart("late")}})
		}).NotTo(Panic())
		Expect(lateErr).To(MatchError(context.Canceled))
		Expect(ctx.Err()).NotTo(HaveOccurred())
	})
})
