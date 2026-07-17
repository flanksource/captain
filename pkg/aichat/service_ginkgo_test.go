package aichat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
)

type fakeResolver struct {
	models   aichat.ModelCatalogResponse
	provider *fakeStreamingProvider
	configs  []api.Config
}

type fakeProviderConfigSource struct {
	backends []api.Backend
	config   api.Config
	requests []aichat.ProviderConfigRequest
	resolve  func(aichat.ProviderConfigRequest) (api.Config, error)
}

func (f *fakeProviderConfigSource) ConfiguredProviders(context.Context) ([]api.Backend, error) {
	return append([]api.Backend(nil), f.backends...), nil
}

func (f *fakeProviderConfigSource) ProviderConfig(_ context.Context, request aichat.ProviderConfigRequest) (api.Config, error) {
	f.requests = append(f.requests, request)
	if f.resolve != nil {
		return f.resolve(request)
	}
	config := request.Config
	config.APIKey = f.config.APIKey
	config.APIURL = f.config.APIURL
	return config, nil
}

func (f *fakeResolver) Models(context.Context) (aichat.ModelCatalogResponse, error) {
	return f.models, nil
}

func (f *fakeResolver) Provider(_ context.Context, config api.Config) (api.StreamingProvider, error) {
	f.configs = append(f.configs, config)
	return f.provider, nil
}

type fakeStreamingProvider struct {
	events  []api.Event
	specs   []api.Spec
	execute func(context.Context, api.Spec) (<-chan api.Event, error)
}

func (f *fakeStreamingProvider) Execute(context.Context, api.Spec) (*api.Response, error) {
	return nil, fmt.Errorf("buffered execution is not used by chat")
}

func (f *fakeStreamingProvider) ExecuteStream(ctx context.Context, spec api.Spec) (<-chan api.Event, error) {
	f.specs = append(f.specs, spec)
	if f.execute != nil {
		return f.execute(ctx, spec)
	}
	events := make(chan api.Event, len(f.events))
	for _, event := range f.events {
		events <- event
	}
	close(events)
	return events, nil
}

func (f *fakeStreamingProvider) GetModel() string          { return "test-model" }
func (f *fakeStreamingProvider) GetBackend() api.Backend   { return api.BackendOpenAI }
func (f *fakeStreamingProvider) SupportsCallerTools() bool { return true }

type fakeAttachmentResolver struct{}

func (fakeAttachmentResolver) Resolve(_ context.Context, inputs []aichat.AttachmentInput) ([]api.AttachmentRef, error) {
	refs := make([]api.AttachmentRef, len(inputs))
	for i, input := range inputs {
		refs[i] = api.AttachmentRef{
			ID:       api.AttachmentIDPrefix + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Filename: input.Filename, MediaType: input.MediaType,
		}.WithPreparedContent(api.AttachmentContent{Bytes: []byte("image")})
	}
	return refs, nil
}

func requestJSON(method, path string, body any) *http.Request {
	var payload bytes.Buffer
	Expect(json.NewEncoder(&payload).Encode(body)).To(Succeed())
	return httptest.NewRequest(method, path, &payload)
}

var _ = Describe("Captain aichat service", func() {
	It("annotates the model catalog with request-scoped configured providers", func() {
		resolver := &fakeResolver{models: aichat.ModelCatalogResponse{
			{ID: "anthropic/claude-sonnet", Provider: "anthropic", Label: "Claude"},
			{ID: "openai/gpt", Provider: "openai", Label: "GPT"},
		}}
		source := &fakeProviderConfigSource{backends: []api.Backend{api.BackendOpenAI}}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: resolver, ProviderConfig: source})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/chat/models", nil))
		Expect(response.Code).To(Equal(http.StatusOK))
		var models aichat.ModelCatalogResponse
		Expect(json.Unmarshal(response.Body.Bytes(), &models)).To(Succeed())
		Expect(models).To(HaveLen(2))
		Expect(models[0].Configured).To(BeFalse())
		Expect(models[1].Configured).To(BeTrue())
	})

	It("applies request-scoped credentials after canonical model selection", func() {
		provider := &fakeStreamingProvider{events: []api.Event{
			{Kind: api.EventText, Text: "done"},
			{Kind: api.EventResult, Success: true, Model: "gpt-5.4"},
		}}
		resolver := &fakeResolver{provider: provider}
		source := &fakeProviderConfigSource{
			backends: []api.Backend{api.BackendOpenAI},
			config:   api.Config{APIKey: "request-token", APIURL: "https://tenant-x.example/ai"},
		}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver, ProviderConfig: source,
			Settings: aichat.RuntimeSettingsProviderFunc(func(context.Context) (aichat.RuntimeSettings, error) {
				return aichat.RuntimeSettings{DefaultModel: "api:gpt-5.4"}, nil
			}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "hello"}}}},
		}))
		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(source.requests).To(HaveLen(1))
		Expect(source.requests[0].Model.Backend).To(Equal(api.BackendOpenAI))
		Expect(source.requests[0].Model.Name).To(Equal("gpt-5.4"))
		Expect(resolver.configs).To(HaveLen(1))
		Expect(resolver.configs[0].APIKey).To(Equal("request-token"))
		Expect(resolver.configs[0].APIURL).To(Equal("https://tenant-x.example/ai"))
	})

	It("rejects provider configuration that changes the canonical resolved model", func() {
		resolver := &fakeResolver{provider: &fakeStreamingProvider{}}
		source := &fakeProviderConfigSource{resolve: func(request aichat.ProviderConfigRequest) (api.Config, error) {
			config := request.Config
			config.Model = api.Model{Name: "claude-sonnet-4-6", Backend: api.BackendAnthropic}
			return config, nil
		}}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver, ProviderConfig: source,
			Settings: aichat.RuntimeSettingsProviderFunc(func(context.Context) (aichat.RuntimeSettings, error) {
				return aichat.RuntimeSettings{DefaultModel: "api:gpt-5.4"}, nil
			}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "hello"}}}},
		}))
		Expect(response.Code).To(Equal(http.StatusServiceUnavailable))
		Expect(response.Body.String()).To(ContainSubstring("provider config source changed the resolved chat model"))
		Expect(resolver.configs).To(BeEmpty())
	})

	It("merges request budget overrides without erasing runtime defaults", func() {
		provider := &fakeStreamingProvider{events: []api.Event{
			{Kind: api.EventText, Text: "done"},
			{Kind: api.EventResult, Success: true, Model: "test-model"},
		}}
		resolver := &fakeResolver{provider: provider}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver,
			Settings: aichat.RuntimeSettingsProviderFunc(func(context.Context) (aichat.RuntimeSettings, error) {
				return aichat.RuntimeSettings{DefaultModel: "openai/test-model", Spec: api.Spec{
					Budget: api.Budget{Cost: 5, MaxTokens: 2_000, MaxTurns: 3},
				}}, nil
			}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "hello"}}}},
			Budget:   api.Budget{MaxTokens: 1_000},
		}))
		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(provider.specs).To(HaveLen(1))
		Expect(provider.specs[0].Budget).To(Equal(api.Budget{Cost: 5, MaxTokens: 1_000, MaxTurns: 3}))
	})

	It("rejects exhausted runtime budgets before provider construction", func() {
		resolver := &fakeResolver{provider: &fakeStreamingProvider{}}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver,
			Settings: aichat.RuntimeSettingsProviderFunc(func(context.Context) (aichat.RuntimeSettings, error) {
				return aichat.RuntimeSettings{
					DefaultModel: "openai/test-model", MonthlyBudgetUSD: 10, CurrentMonthCostUSD: 10,
				}, nil
			}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "hello"}}}},
		}))
		Expect(response.Code).To(Equal(http.StatusPaymentRequired))
		Expect(resolver.configs).To(BeEmpty())
	})

	It("serves models and tools from injected Captain seams", func() {
		resolver := &fakeResolver{models: aichat.ModelCatalogResponse{{
			ID: "openai/test-model", Provider: "openai", Label: "Test", Configured: true,
		}}}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver,
			Tools: aichat.ToolProviderFunc(func(context.Context) (aichat.ToolSet, error) {
				return aichat.ToolSet{Definitions: []api.ToolDefinition{{
					Name: "invoice_get", Group: "billing", Description: "Get invoice",
					InputSchema: map[string]any{"type": "object"},
					Handler:     func(context.Context, map[string]any) (any, error) { return nil, nil },
				}}}, nil
			}),
			MCP: aichat.ToolProviderFunc(func(context.Context) (aichat.ToolSet, error) {
				return aichat.ToolSet{Definitions: []api.ToolDefinition{{
					Name: "docs_search", Group: "docs", Description: "Search documentation",
					Handler: func(context.Context, map[string]any) (any, error) { return nil, nil },
				}}}, nil
			}),
		})

		models := httptest.NewRecorder()
		service.Handler().ServeHTTP(models, httptest.NewRequest(http.MethodGet, "/api/chat/models", nil))
		Expect(models.Code).To(Equal(http.StatusOK))
		Expect(models.Body.String()).To(MatchJSON(`[{"id":"openai/test-model","provider":"openai","label":"Test","reasoning":false,"temperature":false,"configured":true,"contextWindow":0,"inputMediaTypes":null}]`))

		tools := httptest.NewRecorder()
		service.Handler().ServeHTTP(tools, httptest.NewRequest(http.MethodGet, "/api/chat/tools", nil))
		Expect(tools.Code).To(Equal(http.StatusOK))
		var catalog aichat.ToolCatalogResponse
		Expect(json.Unmarshal(tools.Body.Bytes(), &catalog)).To(Succeed())
		Expect(catalog.Tools).To(HaveLen(2))
		Expect(catalog.Tools[0].Name).To(Equal("invoice_get"))
		Expect(catalog.Tools[0].Source).To(Equal("custom"))
		Expect(catalog.Tools[1].Name).To(Equal("docs_search"))
		Expect(catalog.Tools[1].Source).To(Equal("mcp"))
	})

	It("maps the HTTP request directly into api.Spec and streams provider events", func() {
		provider := &fakeStreamingProvider{events: []api.Event{
			{Kind: api.EventText, Text: "done"},
			{Kind: api.EventResult, Success: true, Model: "test-model"},
		}}
		resolver := &fakeResolver{provider: provider}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver,
			Settings: aichat.RuntimeSettingsProviderFunc(func(context.Context) (aichat.RuntimeSettings, error) {
				return aichat.RuntimeSettings{
					DefaultModel: "openai/test-model", System: "Use application tools.",
					ProviderConfig: api.Config{APIURL: "https://example.com/ai", ProjectName: "tenant-x"},
				}, nil
			}),
			Attachments: fakeAttachmentResolver{},
			Tools: aichat.ToolProviderFunc(func(context.Context) (aichat.ToolSet, error) {
				return aichat.ToolSet{Definitions: []api.ToolDefinition{{
					Name: "invoice_get", Handler: func(context.Context, map[string]any) (any, error) { return nil, nil },
				}}}, nil
			}),
		})
		request := aichat.ChatRequest{
			Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{
				{Type: "text", Text: "inspect"},
				{Type: "file", URL: "https://example.com/image.png", Filename: "image.png", MediaType: "image/png"},
			}}},
			Context: "invoice editor", ToolPreferences: api.ToolPreferences{"billing": api.ToolModeAsk},
			ReasoningEffort: api.EffortHigh, PermissionMode: api.PermissionAcceptEdits,
		}
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", request))

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(response.Header().Get("x-vercel-ai-ui-message-stream")).To(Equal("v1"))
		Expect(response.Body.String()).To(ContainSubstring(`"delta":"done"`))
		Expect(provider.specs).To(HaveLen(1))
		spec := provider.specs[0]
		Expect(spec.Model.Name).To(Equal("openai/test-model"))
		Expect(spec.Model.Effort).To(Equal(api.EffortHigh))
		Expect(spec.ToolPreferences).To(Equal(api.ToolPreferences{"billing": api.ToolModeAsk}))
		Expect(spec.Permissions.Mode).To(Equal(api.PermissionAcceptEdits))
		Expect(spec.Messages).To(HaveLen(2))
		Expect(spec.Messages[0]).To(Equal(api.Message{Role: api.RoleSystem, Parts: []api.Part{{
			Type: api.PartText, Text: "Use application tools.\n\nCurrent UI context:\ninvoice editor",
		}}}))
		Expect(spec.Messages[1].Parts).To(HaveLen(2))
		Expect(spec.Messages[1].Parts[1].Attachment).NotTo(BeNil())
		Expect(spec.Messages[1].Parts[1].Attachment.IsPrepared()).To(BeTrue())
		Expect(resolver.configs).To(HaveLen(1))
		Expect(resolver.configs[0].Tools).To(HaveLen(1))
		Expect(resolver.configs[0].APIURL).To(Equal("https://example.com/ai"))
		Expect(resolver.configs[0].ProjectName).To(Equal("tenant-x"))
	})

	It("passes a durable approval resume without rebuilding conversation messages", func() {
		state := api.ToolApprovalState{
			Messages: []api.Message{
				{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "pay"}}},
				{Role: api.RoleAssistant, Parts: []api.Part{{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
					ToolCallID: "call-1", Name: "invoice_pay", Input: json.RawMessage(`{"id":"inv-1"}`),
				}}}},
			},
			Calls: []api.ToolApprovalCall{{Request: api.ToolApprovalRequest{
				ToolCallID: "call-1", Tool: "invoice_pay", Input: json.RawMessage(`{"id":"inv-1"}`),
			}}},
		}
		resume := &api.ToolApprovalResume{State: state, Decisions: []api.ToolApprovalDecision{{
			ToolCallID: "call-1", Tool: "invoice_pay", Action: api.ToolApprovalApprove,
		}}}
		provider := &fakeStreamingProvider{events: []api.Event{{Kind: api.EventResult, Success: true}}}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}})
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Model: "openai/test-model", ToolApproval: resume,
		}))

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(provider.specs).To(HaveLen(1))
		Expect(provider.specs[0].ToolApproval).To(Equal(resume))
		Expect(provider.specs[0].Messages).To(BeNil())
	})

	It("serves thread CRUD through the injected persistence store", func() {
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{}, Threads: aichat.NewMemoryThreadStore()})
		create := httptest.NewRecorder()
		service.Handler().ServeHTTP(create, requestJSON(http.MethodPost, "/api/chat/threads", map[string]string{"title": "Review"}))
		Expect(create.Code).To(Equal(http.StatusCreated))
		var thread aichat.Thread
		Expect(json.Unmarshal(create.Body.Bytes(), &thread)).To(Succeed())

		get := httptest.NewRecorder()
		service.Handler().ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/api/chat/threads/"+thread.ID, nil))
		Expect(get.Code).To(Equal(http.StatusOK))

		remove := httptest.NewRecorder()
		service.Handler().ServeHTTP(remove, httptest.NewRequest(http.MethodDelete, "/api/chat/threads/"+thread.ID, nil))
		Expect(remove.Code).To(Equal(http.StatusNoContent))
	})

	It("persists the completed assistant event stream with tool results and metadata", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Review")
		Expect(err).NotTo(HaveOccurred())
		usage := &api.Usage{InputTokens: 12, OutputTokens: 4}
		provider := &fakeStreamingProvider{events: []api.Event{
			{Kind: api.EventText, Text: "checking"},
			{Kind: api.EventToolUse, Tool: "invoice_get", ToolCallID: "call-1", Input: map[string]any{"id": "inv-1"}},
			{Kind: api.EventToolResult, Tool: "invoice_get", ToolCallID: "call-1", Text: `{"status":"draft"}`, Success: true},
			{Kind: api.EventResult, Success: true, SessionID: "session-1", Model: "test-model", Usage: usage, CostUSD: 0.25},
		}}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}, Threads: store})
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: "chat-1", ThreadID: thread.ID, Model: "openai/test-model",
			Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "inspect"}}}},
		}))
		Expect(response.Code).To(Equal(http.StatusOK))

		stored, err := store.Get(context.Background(), thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Messages).To(HaveLen(2))
		assistant := stored.Messages[1]
		Expect(assistant.Role).To(Equal("assistant"))
		Expect(assistant.Parts).To(HaveLen(3))
		Expect(assistant.Parts[0].Type).To(Equal("text"))
		Expect(assistant.Parts[0].Text).To(Equal("checking"))
		Expect(assistant.Parts[1].Type).To(Equal("dynamic-tool"))
		Expect(assistant.Parts[1].State).To(Equal("output-available"))
		Expect(assistant.Parts[1].ToolCallID).To(Equal("call-1"))
		Expect(assistant.Parts[1].Output).To(MatchJSON(`{"status":"draft"}`))
		Expect(assistant.Parts[2].Type).To(Equal("data-result"))
		Expect(assistant.Metadata).NotTo(BeNil())
		Expect(assistant.Metadata.ProviderSessionID).To(Equal("session-1"))
		Expect(stored.ProviderSessionID).To(Equal("session-1"))
		Expect(stored.TotalInputTokens).To(Equal(12))
		Expect(stored.TotalCostUSD).To(Equal(0.25))
	})

	It("cancels the provider and persistence forwarder when the SSE consumer stops", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Cancellation")
		Expect(err).NotTo(HaveOccurred())
		exited := make(chan struct{})
		provider := &fakeStreamingProvider{}
		provider.execute = func(ctx context.Context, _ api.Spec) (<-chan api.Event, error) {
			events := make(chan api.Event)
			go func() {
				defer close(exited)
				defer close(events)
				for _, event := range []api.Event{
					{Kind: api.EventToolUse, Tool: "invoice_get", ToolCallID: "call-1"},
					{Kind: api.EventToolUse, Tool: "invoice_get", ToolCallID: "call-1"},
					{Kind: api.EventText, Text: "must not block"},
				} {
					select {
					case events <- event:
					case <-ctx.Done():
						return
					}
				}
			}()
			return events, nil
		}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}, Threads: store})
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ThreadID: thread.ID, Model: "openai/test-model",
			Messages: []aichat.UIMessage{{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "inspect"}}}},
		}))
		Eventually(exited).Should(BeClosed())
		Expect(response.Body.String()).To(ContainSubstring("persist duplicate tool call"))
	})
})
