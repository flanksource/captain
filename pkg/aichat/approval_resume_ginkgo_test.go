package aichat_test

import (
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

type approvalFixture struct {
	state     api.ToolApprovalState
	user      aichat.UIMessage
	assistant aichat.UIMessage
}

func newApprovalFixture(approved ...bool) approvalFixture {
	requests := make([]api.Part, len(approved))
	calls := make([]api.ToolApprovalCall, len(approved))
	parts := make([]aichat.UIPart, len(approved))
	for i, allow := range approved {
		callID := fmt.Sprintf("call-%02d", i+1)
		tool := fmt.Sprintf("example_tool_%02d", i+1)
		input := json.RawMessage(fmt.Sprintf(`{"index":%d}`, i+1))
		requests[i] = api.Part{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
			ToolCallID: callID, Name: tool, Input: input,
		}}
		calls[i] = api.ToolApprovalCall{Request: api.ToolApprovalRequest{
			ToolCallID: callID, Tool: tool, Input: input,
		}}
		allowCopy := allow
		parts[i] = aichat.UIPart{
			Type: "dynamic-tool", ToolName: tool, ToolCallID: callID,
			State: "approval-responded", Input: input,
			Approval: &aichat.Approval{ID: callID, Approved: &allowCopy},
		}
	}
	user := aichat.UIMessage{ID: "message-user", Role: "user", Parts: []aichat.UIPart{{
		Type: "text", Text: "Run the example tools.",
	}}}
	state := api.ToolApprovalState{
		Messages: []api.Message{
			{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Run the example tools."}}},
			{Role: api.RoleAssistant, Parts: requests},
		},
		Calls: calls,
	}
	raw, err := json.Marshal(state)
	Expect(err).NotTo(HaveOccurred())
	parts = append(parts, aichat.UIPart{Type: "data-tool-approval", Data: raw})
	return approvalFixture{
		state:     state,
		user:      user,
		assistant: aichat.UIMessage{ID: "message-assistant", Role: "assistant", Parts: parts},
	}
}

func suspendedAssistant(fixture approvalFixture) aichat.UIMessage {
	message := fixture.assistant
	message.Parts = append([]aichat.UIPart(nil), fixture.assistant.Parts...)
	for i := range fixture.state.Calls {
		message.Parts[i].State = "approval-requested"
		message.Parts[i].Approval = &aichat.Approval{ID: fixture.state.Calls[i].Request.ToolCallID}
	}
	return message
}

var _ = Describe("AI SDK approval resume", func() {
	It("reconstructs all batched approval decisions from DefaultChatTransport messages", func() {
		const reportedCallCount = 13
		approved := make([]bool, reportedCallCount)
		for i := range approved {
			approved[i] = true
		}
		fixture := newApprovalFixture(approved...)
		provider := &fakeStreamingProvider{events: []api.Event{{Kind: api.EventResult, Success: true}}}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Model: "anthropic/claude-opus-5", Messages: []aichat.UIMessage{fixture.user, fixture.assistant},
		}))

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(provider.specs).To(HaveLen(1))
		resume := provider.specs[0].ToolApproval
		Expect(resume).NotTo(BeNil())
		Expect(provider.specs[0].Messages).To(BeNil())
		Expect(resume.State).To(Equal(fixture.state))
		Expect(resume.Decisions).To(HaveLen(reportedCallCount))
		for i, decision := range resume.Decisions {
			Expect(decision).To(Equal(api.ToolApprovalDecision{
				ToolCallID: fmt.Sprintf("call-%02d", i+1),
				Tool:       fmt.Sprintf("example_tool_%02d", i+1),
				Action:     api.ToolApprovalApprove,
			}))
		}
	})

	It("rejects an approval response without its durable state before provider execution", func() {
		fixture := newApprovalFixture(true)
		fixture.assistant.Parts = fixture.assistant.Parts[:1]
		provider := &fakeStreamingProvider{}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Model: "anthropic/claude-opus-5", Messages: []aichat.UIMessage{fixture.user, fixture.assistant},
		}))

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring("durable tool approval state"))
		Expect(provider.specs).To(BeEmpty())
	})

	DescribeTable("rejects approval responses that do not match durable state",
		func(mutate func(*approvalFixture), want string) {
			fixture := newApprovalFixture(true, true)
			mutate(&fixture)
			provider := &fakeStreamingProvider{}
			service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}})

			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
				Model: "anthropic/claude-opus-5", Messages: []aichat.UIMessage{fixture.user, fixture.assistant},
			}))

			Expect(response.Code).To(Equal(http.StatusBadRequest))
			Expect(response.Body.String()).To(ContainSubstring(want))
			Expect(provider.specs).To(BeEmpty())
		},
		Entry("approval id", func(fixture *approvalFixture) {
			fixture.assistant.Parts[0].Approval.ID = "approval-other"
		}, `tool call "call-01" approval ID is "approval-other"`),
		Entry("tool name", func(fixture *approvalFixture) {
			fixture.assistant.Parts[0].ToolName = "example_tool_other"
		}, `approval names "example_tool_other"`),
		Entry("tool input", func(fixture *approvalFixture) {
			fixture.assistant.Parts[0].Input = json.RawMessage(`{"index":99}`)
		}, `approval input does not match durable state`),
		Entry("incomplete batch", func(fixture *approvalFixture) {
			fixture.assistant.Parts[1].State = "approval-requested"
			fixture.assistant.Parts[1].Approval.Approved = nil
		}, `pending tool call "call-02" has no approval response`),
	)

	It("replaces a suspended thread message with mixed approved and denied results", func() {
		fixture := newApprovalFixture(true, false)
		fixture.assistant.Parts[1].Approval.Reason = "Keep the existing value."
		store := aichat.NewMemoryThreadStore()
		thread, err := store.Create(context.Background(), "Approval")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.AppendMessage(context.Background(), thread.ID, fixture.user)).To(Succeed())
		Expect(store.AppendMessage(context.Background(), thread.ID, suspendedAssistant(fixture))).To(Succeed())

		provider := &fakeStreamingProvider{events: []api.Event{
			{
				Kind: api.EventToolUse, ToolCallID: "call-01", Tool: "example_tool_01",
				Input: map[string]any{"index": float64(1)},
			},
			{
				Kind: api.EventToolResult, ToolCallID: "call-01", Tool: "example_tool_01",
				Text: `{"updated":true}`, Success: true,
			},
			{Kind: api.EventText, Text: "Finished."},
			{Kind: api.EventResult, Success: true, Model: "claude-opus-5"},
		}}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider}, Threads: store,
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: "chat-approval", ThreadID: thread.ID, Model: "anthropic/claude-opus-5",
			Messages: []aichat.UIMessage{fixture.user, fixture.assistant},
		}))

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		Expect(response.Body.String()).To(ContainSubstring(`"type":"tool-output-denied","toolCallId":"call-02"`))
		Expect(provider.specs).To(HaveLen(1))
		Expect(provider.specs[0].ToolApproval.Decisions).To(Equal([]api.ToolApprovalDecision{
			{ToolCallID: "call-01", Tool: "example_tool_01", Action: api.ToolApprovalApprove},
			{
				ToolCallID: "call-02", Tool: "example_tool_02",
				Action: api.ToolApprovalDeny, Message: "Keep the existing value.",
			},
		}))

		stored, err := store.Get(context.Background(), thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Messages).To(HaveLen(2))
		assistant := stored.Messages[1]
		Expect(assistant.ID).To(Equal("message-assistant"))
		Expect(assistant.Parts[0].State).To(Equal("output-available"))
		Expect(assistant.Parts[0].Output).To(MatchJSON(`{"updated":true}`))
		Expect(assistant.Parts[1].State).To(Equal("output-denied"))
		Expect(assistant.Parts).To(ContainElement(SatisfyAll(
			HaveField("Type", "text"),
			HaveField("Text", "Finished."),
		)))

		nextProvider := &fakeStreamingProvider{events: []api.Event{
			{Kind: api.EventText, Text: "Ready."},
			{Kind: api.EventResult, Success: true, Model: "claude-opus-5"},
		}}
		nextService := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: nextProvider}, Threads: store,
		})
		nextMessages := append([]aichat.UIMessage(nil), stored.Messages...)
		nextMessages = append(nextMessages, aichat.UIMessage{
			ID: "message-next", Role: "user",
			Parts: []aichat.UIPart{{Type: "text", Text: "What happened?"}},
		})
		nextResponse := httptest.NewRecorder()
		nextService.Handler().ServeHTTP(nextResponse, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: "chat-next", ThreadID: thread.ID, Model: "anthropic/claude-opus-5",
			Messages: nextMessages,
		}))
		Expect(nextResponse.Code).To(Equal(http.StatusOK), nextResponse.Body.String())
		Expect(nextProvider.specs).To(HaveLen(1))
		Expect(nextProvider.specs[0].ToolApproval).To(BeNil())
	})

	It("rejects unresolved tool parts outside an approval resume", func() {
		provider := &fakeStreamingProvider{}
		service := aichat.NewService(aichat.ServiceOptions{Resolver: &fakeResolver{provider: provider}})
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			Model: "anthropic/claude-opus-5",
			Messages: []aichat.UIMessage{
				{Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Continue."}}},
				{Role: "assistant", Parts: []aichat.UIPart{{
					Type: "dynamic-tool", ToolName: "example_tool", ToolCallID: "call-pending",
					State: "approval-requested", Input: json.RawMessage(`{"id":"record-1"}`),
				}}},
			},
		}))

		Expect(response.Code).To(Equal(http.StatusBadRequest))
		Expect(response.Body.String()).To(ContainSubstring(`tool call "call-pending" is not terminal`))
		Expect(provider.specs).To(BeEmpty())
	})
})
