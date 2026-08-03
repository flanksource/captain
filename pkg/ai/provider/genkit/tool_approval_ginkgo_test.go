package genkit

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
	gk "github.com/firebase/genkit/go/genkit"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Genkit resumable tool approval", func() {
	It("suspends an ask-mode tool instead of executing it without a broker", func() {
		provider := newToolProvider(nil)
		correlation := newToolEventCorrelation()
		correlation.observeRequest(&gkai.ToolRequest{Name: "invoice_update", Ref: "call-update", Input: map[string]any{"amount": 10}})
		events := make([]ai.Event, 0, 2)
		ran := false

		_, err := provider.runTool(context.Background(), api.ToolDefinition{
			Name:              "invoice_update",
			DefaultPermission: api.ToolModeAsk,
			Handler: func(context.Context, map[string]any) (any, error) {
				ran = true
				return "updated", nil
			},
		}, map[string]any{"amount": 10}, func(event ai.Event) { events = append(events, event) }, correlation)

		interrupted, metadata := gkai.IsToolInterruptError(err)
		Expect(interrupted).To(BeTrue())
		Expect(metadata).To(Equal(map[string]any{"approvalRequired": true}))
		Expect(ran).To(BeFalse())
		Expect(events).To(HaveLen(2))
		Expect(events[0].Kind).To(Equal(ai.EventToolUse))
		Expect(events[1].Kind).To(Equal(ai.EventPermission))
		Expect(events[1].ToolCallID).To(Equal("call-update"))
	})

	It("serializes an interrupted turn with pending and completed sibling calls", func() {
		request := api.Spec{Prompt: api.Prompt{User: "Update then inspect."}}
		pending := gkai.NewToolRequestPart(&gkai.ToolRequest{Name: "invoice_update", Ref: "call-update", Input: map[string]any{"amount": 10}})
		pending.Metadata = map[string]any{"interrupt": map[string]any{"approvalRequired": true}}
		completed := gkai.NewToolRequestPart(&gkai.ToolRequest{Name: "invoice_get", Ref: "call-read", Input: map[string]any{"id": "inv-1"}})
		completed.Metadata = map[string]any{"pendingOutput": map[string]any{"amount": 10}}
		response := &gkai.ModelResponse{
			Request: &gkai.ModelRequest{Messages: []*gkai.Message{gkai.NewUserTextMessage("Update then inspect.")}},
			Message: gkai.NewModelMessage(pending, completed), FinishReason: gkai.FinishReasonInterrupted,
		}

		state, err := toolApprovalState(request, response)
		Expect(err).NotTo(HaveOccurred())
		Expect(state.Validate()).To(Succeed())
		Expect(state.Messages).To(HaveLen(2))
		Expect(state.Pending()).To(Equal([]api.ToolApprovalRequest{{
			ToolCallID: "call-update", Tool: "invoice_update", Input: json.RawMessage(`{"amount":10}`),
		}}))
		Expect(state.Calls[1].Result).NotTo(BeNil())
		Expect(state.Calls[1].Result.Output).To(MatchJSON(`{"amount":10}`))
	})

	It("round trips Gemini thought signatures through the private approval checkpoint", func() {
		signature := []byte("gemini-thought-signature")
		pending := gkai.NewToolRequestPart(&gkai.ToolRequest{
			Name: "accounts_edit", Ref: "call-signed", Input: map[string]any{"id": "acc-1"},
		})
		pending.Metadata = map[string]any{
			"interrupt": map[string]any{"approvalRequired": true},
			"signature": signature,
		}
		response := &gkai.ModelResponse{
			Request: &gkai.ModelRequest{Messages: []*gkai.Message{gkai.NewUserTextMessage("Edit the account")}},
			Message: gkai.NewModelMessage(pending), FinishReason: gkai.FinishReasonInterrupted,
		}

		state, err := toolApprovalState(api.Spec{Prompt: api.Prompt{User: "Edit the account"}}, response)
		Expect(err).NotTo(HaveOccurred())
		Expect(state.ProviderCheckpoint).NotTo(BeNil())

		messages, _, _, err := prepareToolApprovalResume(&api.ToolApprovalResume{
			State: *state,
			Decisions: []api.ToolApprovalDecision{{
				ToolCallID: "call-signed", Tool: "accounts_edit", Action: api.ToolApprovalApprove,
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		requests := genkitApprovalRequests(messages[len(messages)-1])
		Expect(requests["call-signed"].Metadata["signature"]).To(Equal(signature))
	})

	It("restarts an approved call with edited input and never replays completed siblings", func(ctx SpecContext) {
		var updateRuns atomic.Int32
		var readRuns atomic.Int32
		var updatedAmount atomic.Int32
		genkit := gk.Init(ctx)
		modelRef := "test/resumable-approval"
		gk.DefineModel(genkit, modelRef, &gkai.ModelOptions{Supports: &gkai.ModelSupports{Tools: true, Multiturn: true}},
			func(_ context.Context, request *gkai.ModelRequest, stream gkai.ModelStreamCallback) (*gkai.ModelResponse, error) {
				last := request.Messages[len(request.Messages)-1]
				if last.Role == gkai.RoleTool {
					if stream != nil {
						Expect(stream(ctx, &gkai.ModelResponseChunk{Role: gkai.RoleModel, Content: []*gkai.Part{gkai.NewTextPart("done")}})).To(Succeed())
					}
					return &gkai.ModelResponse{Message: gkai.NewModelTextMessage("done"), FinishReason: gkai.FinishReasonStop}, nil
				}
				message := gkai.NewModelMessage(
					gkai.NewToolRequestPart(&gkai.ToolRequest{Name: "invoice_get", Ref: "call-read", Input: map[string]any{"id": "inv-1"}}),
					gkai.NewToolRequestPart(&gkai.ToolRequest{Name: "invoice_update", Ref: "call-update", Input: map[string]any{"amount": 10}}),
				)
				if stream != nil {
					Expect(stream(ctx, &gkai.ModelResponseChunk{Role: gkai.RoleModel, Content: message.Content})).To(Succeed())
				}
				return &gkai.ModelResponse{Message: message, FinishReason: gkai.FinishReasonStop}, nil
			})

		provider := &Provider{
			cfg: ai.Config{
				Model: api.Model{Name: "resumable-approval", Backend: api.BackendOpenAI},
				Tools: []api.ToolDefinition{
					{Name: "invoice_get", DefaultPermission: api.ToolModeOn, Handler: func(context.Context, map[string]any) (any, error) {
						readRuns.Add(1)
						return map[string]any{"amount": 10}, nil
					}},
					{Name: "invoice_update", DefaultPermission: api.ToolModeAsk, Handler: func(_ context.Context, input map[string]any) (any, error) {
						updateRuns.Add(1)
						updatedAmount.Store(int32(input["amount"].(float64)))
						return map[string]any{"updated": true}, nil
					}},
				},
			},
			backend: api.BackendOpenAI,
			g:       genkit, modelRef: modelRef,
		}

		firstStream, err := provider.ExecuteStream(ctx, api.Spec{Prompt: api.Prompt{User: "Update then inspect."}})
		Expect(err).NotTo(HaveOccurred())
		var state *api.ToolApprovalState
		for event := range firstStream {
			Expect(event.Kind).NotTo(Equal(ai.EventError), event.Error)
			if event.Kind == ai.EventResult {
				state = event.ToolApproval
			}
		}
		Expect(state).NotTo(BeNil())
		Expect(state.Pending()).To(HaveLen(1))
		Expect(readRuns.Load()).To(Equal(int32(1)))
		Expect(updateRuns.Load()).To(BeZero())

		resumedStream, err := provider.ExecuteStream(ctx, api.Spec{ToolApproval: &api.ToolApprovalResume{
			State: *state,
			Decisions: []api.ToolApprovalDecision{{
				ToolCallID: "call-update", Tool: "invoice_update", Action: api.ToolApprovalApprove,
				Input: json.RawMessage(`{"amount":12}`),
			}},
		}})
		Expect(err).NotTo(HaveOccurred())
		var resumed []ai.Event
		sawDone := false
		for event := range resumedStream {
			resumed = append(resumed, event)
			Expect(event.Kind).NotTo(Equal(ai.EventError), event.Error)
			if event.Kind == ai.EventText && event.Text == "done" {
				sawDone = true
			}
		}
		Expect(sawDone).To(BeTrue())
		Expect(resumed[len(resumed)-1].Kind).To(Equal(ai.EventResult))
		Expect(resumed[len(resumed)-1].ToolApproval).To(BeNil())
		Expect(readRuns.Load()).To(Equal(int32(1)))
		Expect(updateRuns.Load()).To(Equal(int32(1)))
		Expect(updatedAmount.Load()).To(Equal(int32(12)))
	})

	It("maps deny and externally-resolved calls to native responses", func() {
		deny := gkai.NewToolRequestPart(&gkai.ToolRequest{
			Name: "invoice_delete", Ref: "call-deny", Input: map[string]any{"id": "inv-1"},
		})
		deny.Metadata = map[string]any{"interrupt": map[string]any{"approvalRequired": true}}
		respond := gkai.NewToolRequestPart(&gkai.ToolRequest{
			Name: "invoice_update", Ref: "call-respond", Input: map[string]any{"amount": 10},
		})
		respond.Metadata = map[string]any{"interrupt": map[string]any{"approvalRequired": true}}
		state, err := toolApprovalState(api.Spec{Prompt: api.Prompt{User: "Change it."}}, &gkai.ModelResponse{
			Request: &gkai.ModelRequest{Messages: []*gkai.Message{gkai.NewUserTextMessage("Change it.")}},
			Message: gkai.NewModelMessage(deny, respond), FinishReason: gkai.FinishReasonInterrupted,
		})
		Expect(err).NotTo(HaveOccurred())
		resume := &api.ToolApprovalResume{State: *state, Decisions: []api.ToolApprovalDecision{
			{ToolCallID: "call-deny", Tool: "invoice_delete", Action: api.ToolApprovalDeny, Message: "keep it"},
			{ToolCallID: "call-respond", Tool: "invoice_update", Action: api.ToolApprovalRespond, Result: &api.ToolResult{
				ToolCallID: "call-respond", Output: json.RawMessage(`{"updated":true}`),
			}},
		}}

		messages, restarts, responses, err := prepareToolApprovalResume(resume)
		Expect(err).NotTo(HaveOccurred())
		Expect(restarts).To(BeEmpty())
		Expect(responses).To(HaveLen(2))
		Expect(messages[len(messages)-1].Role).To(Equal(gkai.RoleModel))
		Expect(responses[0].ToolResponse.Output).To(Equal(map[string]any{"denied": true, "reason": "keep it"}))
		Expect(responses[1].ToolResponse.Output).To(Equal(map[string]any{"updated": true}))
	})
})
