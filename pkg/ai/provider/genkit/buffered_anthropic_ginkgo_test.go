package genkit

import (
	"context"
	"encoding/json"
	"time"

	gkai "github.com/firebase/genkit/go/ai"
	gk "github.com/firebase/genkit/go/genkit"
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/aimock"
	"github.com/flanksource/captain/pkg/aimock/anthropicmock"
	"github.com/flanksource/captain/pkg/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const bufferedAnthropicModel = "claude-sonnet-5-5"

var _ = Describe("Genkit buffered Anthropic", func() {
	It("streams high-effort requests internally and returns complete text, usage and cost", func(ctx SpecContext) {
		provider, server := newBufferedAnthropicProvider(`anthropic:
  - respond:
      text: A complete buffered response assembled from several streaming chunks.
      usage: {input: 14, output: 8, cache_read: 3}
`)
		response, err := provider.Execute(ctx, bufferedAnthropicRequest(api.Prompt{User: "Return a complete response."}))
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Text).To(Equal("A complete buffered response assembled from several streaming chunks."))
		Expect(response.Model).To(Equal(bufferedAnthropicModel))
		Expect(response.Usage).To(Equal(ai.Usage{InputTokens: 14, OutputTokens: 8, CacheReadTokens: 3}))
		Expect(response.CostUSD).To(BeNumerically("~", 0.0001083, 0.000000001))
		Expect(server.Remaining()).To(BeEmpty())
		Eventually(server.Requests).Should(ContainElement(And(
			HaveField("Path", "/v1/messages"), HaveField("Stream", true), HaveField("Miss", ""),
		)))
	})

	DescribeTable("preserves validated structured output", func(ctx SpecContext, reflected bool) {
		provider, _ := newBufferedAnthropicProvider(`anthropic:
  - respond: {text: '{"status":"ok"}'}
`)
		target := &structuredStreamResult{}
		prompt := api.Prompt{User: "Return the status."}
		if reflected {
			prompt.Schema = target
		} else {
			prompt.SchemaJSON = json.RawMessage(`{"type":"object","properties":{"status":{"type":"string"}},"required":["status"],"additionalProperties":false}`)
		}
		response, err := provider.Execute(ctx, bufferedAnthropicRequest(prompt))
		Expect(err).NotTo(HaveOccurred())
		if reflected {
			Expect(target).To(Equal(&structuredStreamResult{Status: "ok"}))
			Expect(response.StructuredData).To(Equal(target))
			Expect(response.Text).To(BeEmpty())
		} else {
			Expect(response.StructuredData).To(MatchJSON(`{"status":"ok"}`))
		}
	}, Entry("raw schema", false), Entry("reflected target", true))

	It("classifies invalid final structured output", func(ctx SpecContext) {
		provider, _ := newBufferedAnthropicProvider(`anthropic:
  - respond: {text: '{"status":7}'}
`)
		response, err := provider.Execute(ctx, bufferedAnthropicRequest(api.Prompt{
			User: "Return the status.", SchemaJSON: json.RawMessage(`{"type":"object","properties":{"status":{"type":"string"}},"required":["status"]}`),
		}))
		Expect(response).To(BeNil())
		Expect(err).To(MatchError(ContainSubstring(ai.ErrSchemaValidation.Error())))
	})

	It("preserves provider errors", func(ctx SpecContext) {
		provider, _ := newBufferedAnthropicProvider(`anthropic:
  - respond:
      error: {status: 400, type: invalid_request_error, message: rejected test request}
`)
		response, err := provider.Execute(ctx, bufferedAnthropicRequest(api.Prompt{User: "Return a response."}))
		Expect(response).To(BeNil())
		Expect(err).To(MatchError(ContainSubstring("genkit anthropic generate:")))
		Expect(err).To(MatchError(ContainSubstring("rejected test request")))
	})

	It("preserves buffered tool approval and resumes an approved call once", func(ctx SpecContext) {
		provider, server := newBufferedAnthropicProvider(`anthropic:
  - respond:
      tool_use: {name: record_update, id: toolu_update, input: {value: 10}}
  - match: {tool_result_for: record_update}
    respond: {text: Updated the record.}
`)
		runs := 0
		provider.cfg.Tools = []api.ToolDefinition{{
			Name: "record_update", DefaultPermission: api.ToolPolicyAsk,
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": "number"}}, "required": []string{"value"}},
			Handler: func(_ context.Context, input map[string]any) (any, error) {
				runs++
				Expect(input).To(Equal(map[string]any{"value": float64(12)}))
				return map[string]any{"updated": true}, nil
			},
		}}
		request := bufferedAnthropicRequest(api.Prompt{User: "Update the record."})
		response, err := provider.Execute(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.ToolApproval).NotTo(BeNil())
		Expect(response.ToolApproval.Pending()).To(Equal([]api.ToolApprovalRequest{{ToolCallID: "toolu_update", Tool: "record_update", Input: json.RawMessage(`{"value":10}`)}}))
		Expect(runs).To(BeZero())
		request.Prompt = api.Prompt{}
		request.ToolApproval = &api.ToolApprovalResume{
			State:     *response.ToolApproval,
			Decisions: []api.ToolApprovalDecision{{ToolCallID: "toolu_update", Tool: "record_update", Action: api.ToolApprovalApprove, Input: json.RawMessage(`{"value":12}`)}},
		}
		response, err = provider.Execute(ctx, request)
		Expect(err).NotTo(HaveOccurred())
		Expect(response.ToolApproval).To(BeNil())
		Expect(response.Text).To(Equal("Updated the record."))
		Expect(runs).To(Equal(1))
		Expect(server.Remaining()).To(BeEmpty())
	})

	It("cancels an unfinished stream without returning partial text", func(ctx SpecContext) {
		provider, server := newBufferedAnthropicProvider(`anthropic:
  - respond: {text: Partial response., hold_open_after_content: true}
`)
		requestCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		result := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			response, err := provider.Execute(requestCtx, bufferedAnthropicRequest(api.Prompt{User: "Return a response."}))
			Expect(response).To(BeNil())
			result <- err
		}()
		Eventually(server.Remaining).Should(BeEmpty())
		cancel()
		Eventually(result).Should(Receive(MatchError(ContainSubstring(ai.ErrTimeout.Error()))))
		Eventually(server.Requests).Should(ContainElement(And(HaveField("Path", "/v1/messages"), HaveField("Cancelled", true))))
	}, SpecTimeout(10*time.Second))

	DescribeTable("selects transport without changing the token budget", func(ctx SpecContext, providerType *ai.ModelProvider, streaming bool) {
		genkit := gk.Init(ctx)
		modelRef := "test/buffered-transport"
		gk.DefineModel(genkit, modelRef, &gkai.ModelOptions{},
			func(_ context.Context, request *gkai.ModelRequest, callback gkai.ModelStreamCallback) (*gkai.ModelResponse, error) {
				Expect(callback != nil).To(Equal(streaming))
				if providerType == ai.Anthropic {
					Expect(request.Config).To(HaveKeyWithValue("max_tokens", 28672))
					Expect(request.Config).To(HaveKeyWithValue("output_config", map[string]any{"effort": "high"}))
				}
				return &gkai.ModelResponse{Message: gkai.NewModelTextMessage("complete"), FinishReason: gkai.FinishReasonStop}, nil
			})
		provider := &Provider{cfg: ai.Config{Model: api.Model{Name: bufferedAnthropicModel}}, provider: providerType, g: genkit, modelRef: modelRef}
		response, err := provider.Execute(ctx, bufferedAnthropicRequest(api.Prompt{User: "Return a response."}))
		Expect(err).NotTo(HaveOccurred())
		Expect(response.Text).To(Equal("complete"))
	},
		Entry("Anthropic", ai.Anthropic, true),
		Entry("Google", ai.Google, false),
		Entry("DeepSeek", ai.DeepSeek, false),
		Entry("OpenAI compatibility", ai.OpenAI, false),
	)
})

func newBufferedAnthropicProvider(scenarioYAML string) (*Provider, *anthropicmock.Server) {
	scenario, err := aimock.Parse([]byte(scenarioYAML))
	Expect(err).NotTo(HaveOccurred())
	server, err := anthropicmock.Start(anthropicmock.Options{Scenario: scenario})
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(server.Close)
	provider, err := New(ai.Config{
		Model:  api.Model{Name: bufferedAnthropicModel, Provider: ai.Anthropic, Mode: api.ModeAPI},
		APIKey: aimock.DummyKey, APIURL: server.APIURL(),
	})
	Expect(err).NotTo(HaveOccurred())
	return provider, server
}

func bufferedAnthropicRequest(prompt api.Prompt) api.Spec {
	return api.Spec{
		Model:  api.Model{Name: bufferedAnthropicModel, Provider: ai.Anthropic, Mode: api.ModeAPI, Effort: api.EffortHigh},
		Budget: api.Budget{MaxTokens: 4096}, Prompt: prompt,
	}
}
