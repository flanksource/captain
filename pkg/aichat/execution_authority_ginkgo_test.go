package aichat_test

import (
	"context"
	"encoding/json"
	"errors"
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
	beginErr    error
	begins      []aichat.ExecutionRequest
	resolutions []aichat.ToolApprovalResolution
}

func (f *fakeExecutionAuthority) Begin(_ context.Context, request aichat.ExecutionRequest) (aichat.Execution, error) {
	f.begins = append(f.begins, request)
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	f.execution.turnID = "turn-" + request.RequestID
	return f.execution, nil
}

func (f *fakeExecutionAuthority) ResolveToolApproval(
	_ context.Context,
	resolution aichat.ToolApprovalResolution,
) (*aichat.ApprovalContinuation, error) {
	f.resolutions = append(f.resolutions, resolution)
	return nil, nil
}

type fakeExecution struct {
	events     chan api.Event
	endpoint   *api.CallerToolEndpoint
	observed   []api.Event
	closed     bool
	turnID     string
	interrupts []string
}

func (f *fakeExecution) Interrupt(_ context.Context, reason string) error {
	f.interrupts = append(f.interrupts, reason)
	return nil
}

func (f *fakeExecution) CaptainSessionID() string { return "captain-session-1" }
func (f *fakeExecution) TurnID() string {
	if f.turnID == "" {
		return "turn-1"
	}
	return f.turnID
}
func (f *fakeExecution) PromptRunID() string { return "prompt-run-1" }
func (f *fakeExecution) CallerTools() *api.CallerToolEndpoint {
	if f.endpoint == nil {
		return nil
	}
	endpoint := *f.endpoint
	return &endpoint
}
func (f *fakeExecution) Events() <-chan api.Event { return f.events }
func (f *fakeExecution) Observe(_ context.Context, event api.Event) (api.Event, error) {
	if event.Kind == api.EventPermission && event.ApprovalID == "" {
		event.ApprovalID = "0e5dc2fe-8b77-44e9-a3de-6a00298c8bde"
	}
	f.observed = append(f.observed, event)
	return event, nil
}
func (f *fakeExecution) Close(context.Context) error {
	f.closed = true
	return nil
}

var _ = Describe("Authoritative aichat execution", func() {
	It("interrupts an active turn and finishes its stream without an error", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Interrupt")
		Expect(err).NotTo(HaveOccurred())
		started := make(chan struct{})
		providerInterrupted := make(chan struct{}, 1)
		provider := &fakeStreamingProvider{
			execute: func(ctx context.Context, _ api.Spec) (<-chan api.Event, error) {
				events := make(chan api.Event, 2)
				events <- api.Event{Kind: api.EventSystem, SessionID: "provider-session-1"}
				events <- api.Event{Kind: api.EventText, Text: "partial"}
				close(started)
				go func() {
					<-ctx.Done()
					close(events)
				}()
				return events, nil
			},
			interrupt: func(context.Context) error {
				providerInterrupted <- struct{}{}
				return nil
			},
		}
		execution := &fakeExecution{}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider}, Threads: aichat.FixedThreadStore(store),
			Authority: &fakeExecutionAuthority{execution: execution},
		})

		chatResponse := httptest.NewRecorder()
		chatDone := make(chan struct{})
		go func() {
			defer close(chatDone)
			service.Handler().ServeHTTP(chatResponse, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
				ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
				Runtime: &api.Model{Name: "sonnet", Backend: api.BackendClaudeAgent},
				Messages: []aichat.UIMessage{{
					ID: "user-message-interrupt", Role: "user",
					Parts: []aichat.UIPart{{Type: "text", Text: "Start a long response"}},
				}},
			}))
		}()
		Eventually(started).Should(BeClosed())

		interruptResponse := httptest.NewRecorder()
		service.Handler().ServeHTTP(interruptResponse, httptest.NewRequest(
			http.MethodPost, "/api/chat/sessions/"+thread.ID+"/interrupt", nil,
		))
		Expect(interruptResponse.Code).To(Equal(http.StatusOK), interruptResponse.Body.String())
		Eventually(providerInterrupted).Should(Receive())
		Eventually(chatDone).Should(BeClosed())
		Expect(execution.interrupts).To(Equal([]string{"user"}))
		Expect(chatResponse.Body.String()).To(ContainSubstring(`"interrupted":true`))
		Expect(chatResponse.Body.String()).NotTo(ContainSubstring(`"type":"error"`))

		second := httptest.NewRecorder()
		service.Handler().ServeHTTP(second, httptest.NewRequest(
			http.MethodPost, "/api/chat/sessions/"+thread.ID+"/interrupt", nil,
		))
		Expect(second.Code).To(Equal(http.StatusConflict))
	})

	It("injects the run-bound endpoint and merges its live approval event", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Accounts")
		Expect(err).NotTo(HaveOccurred())
		execution := &fakeExecution{
			events: make(chan api.Event, 1),
			endpoint: &api.CallerToolEndpoint{
				Name: "captain", URL: "http://127.0.0.1:43210/mcp",
				Headers: map[string]string{"Authorization": "Bearer secret"},
			},
		}
		execution.events <- api.Event{
			Kind: api.EventPermission, Tool: "account_edit",
			ToolCallID: "call-account-1", Input: map[string]any{"id": "acc-1"},
		}
		close(execution.events)
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
			Resolver: &fakeResolver{provider: provider}, Threads: aichat.FixedThreadStore(store), Authority: authority,
			Tools: aichat.StaticToolProvider([]api.ToolDefinition{{
				Name: "account_edit", DefaultPermission: api.ToolModeAsk,
				Handler: func(context.Context, map[string]any) (any, error) { return nil, nil },
			}}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "sonnet", Backend: api.BackendClaudeAgent},
			Messages: []aichat.UIMessage{{
				ID: "user-message-1", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Edit the account"}},
			}},
		}))

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(response.Body.String()).To(ContainSubstring(`"type":"tool-approval-request"`))
		Expect(authority.begins).To(HaveLen(1))
		Expect(authority.begins[0].ThreadID).To(Equal(thread.ID))
		Expect(authority.begins[0].RequestID).To(Equal("user-message-1"))
		Expect(provider.specs).To(HaveLen(1))
		Expect(execution.observed).To(ContainElement(
			MatchFields(IgnoreExtras, Fields{"Kind": Equal(api.EventResult)}),
		))
		Expect(execution.closed).To(BeTrue())
	})

	It("streams API-provider approvals without waiting for agent caller-tool events", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Accounts")
		Expect(err).NotTo(HaveOccurred())
		calls := []api.ToolApprovalRequest{
			{ToolCallID: "call-account-1", Tool: "account_edit", Input: json.RawMessage(`{"id":"acc-1"}`)},
			{ToolCallID: "call-account-2", Tool: "account_edit", Input: json.RawMessage(`{"id":"acc-2"}`)},
		}
		execution := &fakeExecution{events: make(chan api.Event)}
		provider := &fakeStreamingProvider{backend: api.BackendGemini, events: []api.Event{
			{Kind: api.EventToolUse, Tool: "account_edit", ToolCallID: calls[0].ToolCallID, Input: map[string]any{"id": "acc-1"}},
			{Kind: api.EventToolUse, Tool: "account_edit", ToolCallID: calls[1].ToolCallID, Input: map[string]any{"id": "acc-2"}},
			{Kind: api.EventPermission, Tool: "account_edit", ToolCallID: calls[0].ToolCallID},
			{Kind: api.EventPermission, Tool: "account_edit", ToolCallID: calls[1].ToolCallID},
			{Kind: api.EventResult, Success: true, ToolApproval: pendingApprovalState(calls...)},
		}}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider}, Threads: aichat.FixedThreadStore(store),
			Authority: &fakeExecutionAuthority{execution: execution},
			Tools: aichat.StaticToolProvider([]api.ToolDefinition{{
				Name: "account_edit", DefaultPermission: api.ToolModeAsk,
				Handler: func(context.Context, map[string]any) (any, error) { return nil, nil },
			}}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "gemini", Backend: api.BackendGemini},
			Messages: []aichat.UIMessage{{
				ID: "user-message-approval", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Edit the accounts"}},
			}},
		}))

		parts := decodedDataLines(response.Body.String())
		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(partTypes(parts)).To(Equal([]string{
			"start", "start-step", "tool-input-available", "tool-input-available",
			"tool-approval-request", "tool-approval-request", "data-result", "finish-step", "finish",
		}))
		Expect(parts[0]).To(HaveKeyWithValue("messageId", "turn-user-message-approval-assistant"))
		stored, err := store.Get(context.Background(), thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Messages).To(HaveLen(2))
		Expect(stored.Messages[1].ID).To(Equal(parts[0]["messageId"]))
		Expect(stored.Messages[1].Parts[0].State).To(Equal("approval-requested"))
		Expect(stored.Messages[1].Parts[1].State).To(Equal("approval-requested"))
		Expect(execution.observed).To(ContainElement(MatchFields(IgnoreExtras, Fields{"Kind": Equal(api.EventResult)})))
	})

	It("admits sequential user messages in one Captain thread as distinct turns", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Accounts")
		Expect(err).NotTo(HaveOccurred())
		execution := &fakeExecution{}
		authority := &fakeExecutionAuthority{execution: execution}
		provider := &fakeStreamingProvider{events: []api.Event{
			{Kind: api.EventText, Text: "Done."},
			{Kind: api.EventResult, Success: true},
		}}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider}, Threads: aichat.FixedThreadStore(store), Authority: authority,
		})

		messages := []aichat.UIMessage{{
			ID: "user-message-1", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "List accounts"}},
		}}
		first := httptest.NewRecorder()
		service.Handler().ServeHTTP(first, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "gemini", Backend: api.BackendGemini}, Messages: messages,
		}))
		Expect(first.Code).To(Equal(http.StatusOK), first.Body.String())

		persisted, err := store.Get(context.Background(), thread.ID)
		Expect(err).NotTo(HaveOccurred())
		messages = append(persisted.Messages, aichat.UIMessage{
			ID: "user-message-2", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "List contacts"}},
		})
		second := httptest.NewRecorder()
		service.Handler().ServeHTTP(second, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "gemini", Backend: api.BackendGemini}, Messages: messages,
		}))
		Expect(second.Code).To(Equal(http.StatusOK), second.Body.String())

		Expect(authority.begins).To(HaveLen(2))
		Expect(authority.begins[0].RequestID).To(Equal("user-message-1"))
		Expect(authority.begins[1].RequestID).To(Equal("user-message-2"))
		persisted, err = store.Get(context.Background(), thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(persisted.Messages).To(HaveLen(4))
		Expect(persisted.Messages[1].ID).To(Equal("turn-user-message-1-assistant"))
		Expect(persisted.Messages[3].ID).To(Equal("turn-user-message-2-assistant"))
	})

	It("regenerates the named assistant message without duplicating persisted history", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Accounts")
		Expect(err).NotTo(HaveOccurred())
		user := aichat.UIMessage{
			ID: "user-message-1", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "List accounts"}},
		}
		assistant := aichat.UIMessage{
			ID: "user-message-1-assistant", Role: "assistant", Parts: []aichat.UIPart{{Type: "text", Text: "Old answer"}},
		}
		Expect(store.AppendMessage(context.Background(), thread.ID, user)).To(Succeed())
		Expect(store.AppendMessage(context.Background(), thread.ID, assistant)).To(Succeed())
		authority := &fakeExecutionAuthority{execution: &fakeExecution{}}
		provider := &fakeStreamingProvider{events: []api.Event{
			{Kind: api.EventText, Text: "New answer"},
			{Kind: api.EventResult, Success: true},
		}}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider}, Threads: aichat.FixedThreadStore(store), Authority: authority,
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "regenerate-message", MessageID: assistant.ID,
			Runtime: &api.Model{Name: "gemini", Backend: api.BackendGemini}, Messages: []aichat.UIMessage{user},
		}))

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(authority.begins).To(HaveLen(1))
		Expect(authority.begins[0].RequestID).To(Equal(assistant.ID))
		persisted, err := store.Get(context.Background(), thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(persisted.Messages).To(HaveLen(2))
		Expect(persisted.Messages[1].ID).To(Equal(assistant.ID))
		Expect(persisted.Messages[1].Parts).To(ContainElement(HaveField("Text", "New answer")))
	})

	It("rejects a persisted chat id that differs from its Captain thread", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Accounts")
		Expect(err).NotTo(HaveOccurred())
		service := aichat.NewService(aichat.ServiceOptions{Threads: aichat.FixedThreadStore(store)})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: "different-chat", ThreadID: thread.ID, Trigger: "submit-message",
			Messages: []aichat.UIMessage{{ID: "user-message-1", Role: "user"}},
		}))

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring("must match threadId"))
	})

	It("does not persist the user message when execution admission fails", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Accounts")
		Expect(err).NotTo(HaveOccurred())
		authority := &fakeExecutionAuthority{beginErr: errors.New("duplicate prompt run")}
		service := aichat.NewService(aichat.ServiceOptions{
			Threads: aichat.FixedThreadStore(store), Authority: authority,
			Resolver: &fakeResolver{provider: &fakeStreamingProvider{backend: api.BackendGemini}},
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "gemini", Backend: api.BackendGemini},
			Messages: []aichat.UIMessage{{
				ID: "user-message-1", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "List accounts"}},
			}},
		}))

		Expect(response.Code).To(Equal(http.StatusInternalServerError))
		persisted, err := store.Get(context.Background(), thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(persisted.Messages).To(BeEmpty())
	})

	It("resolves an approval only after authorizing its thread", func() {
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Contacts")
		Expect(err).NotTo(HaveOccurred())
		authority := &fakeExecutionAuthority{}
		service := aichat.NewService(aichat.ServiceOptions{Threads: aichat.FixedThreadStore(store), Authority: authority})
		response := httptest.NewRecorder()

		approvalID := "0e5dc2fe-8b77-44e9-a3de-6a00298c8bde"
		service.Handler().ServeHTTP(response, requestJSON(
			http.MethodPost,
			"/api/chat/sessions/"+thread.ID+"/approvals/"+approvalID,
			map[string]any{"approved": true, "updatedInput": map[string]any{"name": "Acme"}},
		))

		Expect(response.Code).To(Equal(http.StatusOK))
		Expect(authority.resolutions).To(HaveLen(1))
		Expect(authority.resolutions[0].ThreadID).To(Equal(thread.ID))
		Expect(authority.resolutions[0].ApprovalID).To(Equal(approvalID))
		Expect(authority.resolutions[0].UpdatedInput).To(Equal(map[string]any{"name": "Acme"}))

		missing := httptest.NewRecorder()
		service.Handler().ServeHTTP(missing, requestJSON(
			http.MethodPost,
			"/api/chat/sessions/missing/approvals/"+approvalID,
			json.RawMessage(`{"approved":false}`),
		))
		Expect(missing.Code).To(Equal(http.StatusNotFound))
		Expect(authority.resolutions).To(HaveLen(1))
	})
})
