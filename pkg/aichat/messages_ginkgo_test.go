package aichat_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
)

// apiRuntime pins the api mode: message projection is the api path's contract
// (the agent path carries a single prompt instead), and a bare catalog id now
// takes openai's default mode, which is agent.
var apiRuntime = &api.Model{Name: "openai/test-model", Mode: api.ModeAPI}

var _ = Describe("chat message projection", func() {
	DescribeTable("omits assistant messages without provider content",
		func(parts []aichat.UIPart) {
			provider := &fakeStreamingProvider{events: []api.Event{{Kind: api.EventResult, Success: true}}}
			service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}})
			response := httptest.NewRecorder()

			service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
				Runtime: apiRuntime,
				Messages: []aichat.UIMessage{
					{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Review the transaction."}}},
					{Role: "assistant", Parts: parts},
					{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Try again."}}},
				},
			}))

			Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
			Expect(provider.specs).To(HaveLen(1))
			Expect(provider.specs[0].Messages).To(Equal([]api.Message{
				{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Review the transaction."}}},
				{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Try again."}}},
			}))
		},
		Entry("empty assistant shell", []aichat.UIPart{}),
		Entry("step boundary only", []aichat.UIPart{{Type: "step-start"}}),
		Entry("persisted error data only", []aichat.UIPart{{
			Type: "data-error", Data: json.RawMessage(`{"error":"provider disconnected"}`),
		}}),
	)

	It("retains assistant provider content alongside UI-only parts", func() {
		provider := &fakeStreamingProvider{events: []api.Event{{Kind: api.EventResult, Success: true}}}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}})
		response := httptest.NewRecorder()

		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Runtime: apiRuntime,
			Messages: []aichat.UIMessage{
				{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Review the transaction."}}},
				{Role: "assistant", Parts: []aichat.UIPart{
					{Type: "step-start"},
					{Type: "text", Text: "The stream started."},
					{Type: "data-result", Data: json.RawMessage(`{"success":true}`)},
				}},
				{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Continue."}}},
			},
		}))

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(provider.specs).To(HaveLen(1))
		Expect(provider.specs[0].Messages).To(Equal([]api.Message{
			{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Review the transaction."}}},
			{Role: api.RoleAssistant, Parts: []api.Part{{Type: api.PartText, Text: "The stream started."}}},
			{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Continue."}}},
		}))
	})

	It("rejects a user message without provider content", func() {
		provider := &fakeStreamingProvider{}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}})
		response := httptest.NewRecorder()

		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Runtime:  apiRuntime,
			Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "step-start"}}}},
		}))

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring("message 1 (user) has no provider content"))
		Expect(provider.specs).To(BeEmpty())
	})

	It("rejects unsupported assistant parts", func() {
		provider := &fakeStreamingProvider{}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}})
		response := httptest.NewRecorder()

		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Runtime: apiRuntime,
			Messages: []aichat.UIMessage{
				{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Review the transaction."}}},
				{Role: "assistant", Parts: []aichat.UIPart{{Type: "finish-step"}}},
				{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Continue."}}},
			},
		}))

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring(`message 2 part 1: unsupported AI SDK part type "finish-step"`))
		Expect(provider.specs).To(BeEmpty())
	})
})
