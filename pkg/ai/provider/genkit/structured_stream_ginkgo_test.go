package genkit

import (
	"context"
	"encoding/json"

	gkai "github.com/firebase/genkit/go/ai"
	gk "github.com/firebase/genkit/go/genkit"
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type structuredStreamResult struct {
	Status string `json:"status"`
}

var _ = Describe("Genkit structured streaming", func() {
	DescribeTable("returns only terminal structured data",
		func(ctx SpecContext, prompt func() (api.Prompt, func())) {
			provider := newStructuredStreamingProvider(ctx, `{"status":"ok"}`)
			requestPrompt, assertBound := prompt()
			events, err := provider.ExecuteStream(ctx, api.Spec{Prompt: requestPrompt})
			Expect(err).NotTo(HaveOccurred())

			var result *ai.Event
			for event := range events {
				Expect(event.Kind).NotTo(Equal(ai.EventError), event.Error)
				Expect(event.Kind).NotTo(Equal(ai.EventText), "structured JSON must not be rendered as text")
				if event.Kind == ai.EventResult {
					copy := event
					result = &copy
				}
			}
			Expect(result).NotTo(BeNil())
			Expect(result.Success).To(BeTrue())
			Expect(result.Model).To(Equal("structured-stream"))
			Expect(result.Usage).To(Equal(&ai.Usage{InputTokens: 3, OutputTokens: 5}))
			Expect(result.StructuredData).To(MatchJSON(`{"status":"ok"}`))
			assertBound()
		},
		Entry("from a reflected Go target", func() (api.Prompt, func()) {
			target := &structuredStreamResult{}
			return api.Prompt{User: "Return the status.", Schema: target}, func() {
				Expect(target).To(Equal(&structuredStreamResult{Status: "ok"}))
			}
		}),
		Entry("from a raw JSON schema", func() (api.Prompt, func()) {
			return api.Prompt{
				User: "Return the status.",
				SchemaJSON: json.RawMessage(`{
					"type":"object",
					"properties":{"status":{"type":"string"}},
					"required":["status"],
					"additionalProperties":false
				}`),
			}, func() {}
		}),
	)

	It("classifies invalid final output as a schema-validation error", func(ctx SpecContext) {
		provider := newStructuredStreamingProvider(ctx, `{"status":7}`)
		events, err := provider.ExecuteStream(ctx, api.Spec{Prompt: api.Prompt{
			User: "Return the status.",
			SchemaJSON: json.RawMessage(`{
				"type":"object",
				"properties":{"status":{"type":"string"}},
				"required":["status"]
			}`),
		}})
		Expect(err).NotTo(HaveOccurred())

		var got []ai.Event
		for event := range events {
			got = append(got, event)
		}
		Expect(got).To(HaveLen(1))
		Expect(got[0].Kind).To(Equal(ai.EventError))
		Expect(got[0].Error).To(ContainSubstring(ai.ErrSchemaValidation.Error()))
	})
})

func newStructuredStreamingProvider(ctx context.Context, response string) *Provider {
	genkit := gk.Init(ctx)
	modelRef := "test/structured-stream"
	gk.DefineModel(genkit, modelRef, &gkai.ModelOptions{
		Supports: &gkai.ModelSupports{Constrained: gkai.ConstrainedSupportAll},
	}, func(ctx context.Context, request *gkai.ModelRequest, stream gkai.ModelStreamCallback) (*gkai.ModelResponse, error) {
		Expect(request.Output).NotTo(BeNil())
		Expect(request.Output.Schema).NotTo(BeNil())
		Expect(stream).NotTo(BeNil())
		Expect(stream(ctx, &gkai.ModelResponseChunk{
			Role: gkai.RoleModel, Content: []*gkai.Part{gkai.NewTextPart(response)},
		})).To(Succeed())
		return &gkai.ModelResponse{
			Message:      gkai.NewModelTextMessage(response),
			FinishReason: gkai.FinishReasonStop,
			Usage:        &gkai.GenerationUsage{InputTokens: 3, OutputTokens: 5},
		}, nil
	})
	return &Provider{
		cfg:      ai.Config{Model: api.Model{Name: "structured-stream", Mode: api.ModeAPI}},
		provider: ai.Google,
		g:        genkit, modelRef: modelRef,
	}
}
