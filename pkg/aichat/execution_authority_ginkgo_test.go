package aichat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

type fakeExecutionAuthority struct {
	execution   *fakeExecution
	begins      []aichat.ExecutionRequest
	resolutions []aichat.ToolApprovalResolution
}

func (f *fakeExecutionAuthority) Begin(_ context.Context, request aichat.ExecutionRequest) (aichat.Execution, error) {
	f.begins = append(f.begins, request)
	return f.execution, nil
}

func (f *fakeExecutionAuthority) ResolveToolApproval(
	_ context.Context,
	resolution aichat.ToolApprovalResolution,
) error {
	f.resolutions = append(f.resolutions, resolution)
	return nil
}

type fakeExecution struct {
	events   chan api.Event
	endpoint api.CallerToolEndpoint
	observed []api.Event
	closed   bool
}

func (f *fakeExecution) CaptainSessionID() string { return "captain-session-1" }
func (f *fakeExecution) PromptRunID() string      { return "prompt-run-1" }
func (f *fakeExecution) CallerTools() *api.CallerToolEndpoint {
	endpoint := f.endpoint
	return &endpoint
}
func (f *fakeExecution) Events() <-chan api.Event { return f.events }
func (f *fakeExecution) Observe(_ context.Context, event api.Event) error {
	f.observed = append(f.observed, event)
	return nil
}
func (f *fakeExecution) Close(context.Context) error {
	f.closed = true
	return nil
}

var _ = Describe("Authoritative aichat execution", func() {
	It("injects the run-bound endpoint and merges its live approval event", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Accounts")
		Expect(err).NotTo(HaveOccurred())
		execution := &fakeExecution{
			events: make(chan api.Event, 1),
			endpoint: api.CallerToolEndpoint{
				Name: "captain", URL: "http://127.0.0.1:43210/mcp",
				Headers: map[string]string{"Authorization": "Bearer secret"},
			},
		}
		execution.events <- api.Event{
			Kind: api.EventPermission, Tool: "account_edit",
			ToolCallID: "call-account-1", Input: map[string]any{"id": "acc-1"},
		}
		authority := &fakeExecutionAuthority{execution: execution}
		provider := &fakeStreamingProvider{
			backend: api.BackendClaudeAgent,
			events: []api.Event{
				{Kind: api.EventToolUse, Tool: "account_edit", ToolCallID: "call-account-1", Input: map[string]any{"id": "acc-1"}},
				{Kind: api.EventToolResult, Tool: "account_edit", ToolCallID: "call-account-1", Text: `{"updated":true}`, Success: true},
				{Kind: api.EventResult, Success: true, SessionID: "provider-session-1"},
			},
		}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider}, Threads: store, Authority: authority,
			Tools: aichat.StaticToolProvider([]api.ToolDefinition{{
				Name: "account_edit", DefaultPermission: api.ToolModeAsk,
				Handler: func(context.Context, map[string]any) (any, error) { return nil, nil },
			}}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: "chat-1", ThreadID: thread.ID,
			Runtime: &api.Model{Name: "sonnet", Backend: api.BackendClaudeAgent},
			Messages: []aichat.UIMessage{{
				Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Edit the account"}},
			}},
		}))

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(response.Body.String()).To(ContainSubstring(`"type":"tool-approval-request"`))
		Expect(authority.begins).To(HaveLen(1))
		Expect(authority.begins[0].ThreadID).To(Equal(thread.ID))
		Expect(provider.specs).To(HaveLen(1))
		Expect(execution.observed).To(ContainElement(
			MatchFields(IgnoreExtras, Fields{"Kind": Equal(api.EventResult)}),
		))
		Expect(execution.closed).To(BeTrue())
	})

	It("resolves an approval only after authorizing its thread", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Contacts")
		Expect(err).NotTo(HaveOccurred())
		authority := &fakeExecutionAuthority{}
		service := aichat.NewService(aichat.ServiceOptions{Threads: store, Authority: authority})
		response := httptest.NewRecorder()

		service.Handler().ServeHTTP(response, requestJSON(
			http.MethodPost,
			"/api/chat/threads/"+thread.ID+"/approvals/call-contact-1",
			map[string]any{"approved": true, "updatedInput": map[string]any{"name": "Acme"}},
		))

		Expect(response.Code).To(Equal(http.StatusNoContent))
		Expect(authority.resolutions).To(HaveLen(1))
		Expect(authority.resolutions[0].ThreadID).To(Equal(thread.ID))
		Expect(authority.resolutions[0].ToolCallID).To(Equal("call-contact-1"))
		Expect(authority.resolutions[0].UpdatedInput).To(Equal(map[string]any{"name": "Acme"}))

		missing := httptest.NewRecorder()
		service.Handler().ServeHTTP(missing, requestJSON(
			http.MethodPost,
			"/api/chat/threads/missing/approvals/call-contact-1",
			json.RawMessage(`{"approved":false}`),
		))
		Expect(missing.Code).To(Equal(http.StatusNotFound))
		Expect(authority.resolutions).To(HaveLen(1))
	})
})
