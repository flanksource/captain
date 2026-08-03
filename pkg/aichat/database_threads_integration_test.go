package aichat_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/commons-db/dbtest"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Database chat sessions", func() {
	It("keeps provider session identity immutable", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_provider_identity"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		store, err := aichat.NewDatabaseThreadStore(db)
		Expect(err).NotTo(HaveOccurred())
		thread, err := store.Create(ctx, "Provider identity")
		Expect(err).NotTo(HaveOccurred())

		Expect(store.SetProviderSession(ctx, thread.ID, "provider-session-1")).To(Succeed())
		bound, err := store.GetSession(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.SetProviderSession(ctx, thread.ID, "provider-session-1")).To(Succeed())
		replayed, err := store.GetSession(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed.Revision).To(Equal(bound.Revision))
		Expect(store.SetProviderSession(ctx, thread.ID, "provider-session-2")).To(MatchError(ContainSubstring(
			`provider session ID is already bound to "provider-session-1"`,
		)))
	})

	It("projects messages, turns, usage, and durable approvals from one Captain session", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_session_projection"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		store, err := aichat.NewDatabaseThreadStore(db)
		Expect(err).NotTo(HaveOccurred())
		thread, err := store.Create(ctx, "Accounts")
		Expect(err).NotTo(HaveOccurred())
		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		execution, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: thread.ID, RequestID: "user-message-1", Title: thread.Title,
			Spec: api.Spec{Model: api.Model{Name: "gemini", Backend: api.BackendGemini}.Capabilities()},
		})
		Expect(err).NotTo(HaveOccurred())

		user := aichat.UIMessage{
			ID: "user-message-1", TurnID: execution.TurnID(), Role: "user",
			Parts: []aichat.UIPart{{Type: "text", Text: "Edit the account"}},
		}
		Expect(store.AppendMessage(ctx, thread.ID, user)).To(Succeed())
		permission, err := execution.Observe(ctx, api.Event{
			Kind: api.EventPermission, ToolCallID: "call-account-1", Tool: "accounts_edit",
			Input: map[string]any{"id": "acc-1"},
		})
		Expect(err).NotTo(HaveOccurred())
		assistant := aichat.UIMessage{
			ID: execution.TurnID() + "-assistant", TurnID: execution.TurnID(), Role: "assistant",
			Parts: []aichat.UIPart{{
				Type: "dynamic-tool", ToolName: "accounts_edit", ToolCallID: "call-account-1",
				State: "approval-requested", Input: json.RawMessage(`{"id":"acc-1"}`),
				Approval: &aichat.Approval{ID: permission.ApprovalID},
			}},
		}
		Expect(store.AppendMessage(ctx, thread.ID, assistant)).To(Succeed())

		aggregate, err := store.GetSession(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(aggregate.ID).To(Equal(thread.ID))
		Expect(aggregate.Revision).To(BeNumerically(">", 0))
		Expect(aggregate.Messages).To(HaveLen(2))
		Expect(aggregate.Messages[0].TurnID).To(Equal(execution.TurnID()))
		Expect(aggregate.Turns).To(HaveLen(1))
		Expect(aggregate.Requests).To(HaveLen(1))
		Expect(aggregate.Requests[0].ID).To(Equal(permission.ApprovalID))
		Expect(aggregate.Requests[0].TurnID).To(Equal(execution.TurnID()))
		Expect(aggregate.Requests[0].PromptRunID).To(Equal(execution.PromptRunID()))
		Expect(aggregate.Requests[0].ToolCallID).To(Equal("call-account-1"))
		Expect(aggregate.Requests[0].Kind).To(Equal("tool_approval"))
		Expect(aggregate.Requests[0].State).To(Equal("pending"))
		Expect(aggregate.Requests[0].Tool).To(Equal("accounts_edit"))
		Expect(aggregate.Requests[0].Input).To(MatchJSON(`{"id":"acc-1"}`))
		Expect(aggregate.Requests[0].RequestedBy).To(Equal("provider"))
		Expect(aggregate.Messages[1].Parts[0].Approval.ID).To(Equal(permission.ApprovalID))

		continuation, err := authority.ResolveToolApproval(ctx, aichat.ToolApprovalResolution{
			ThreadID: thread.ID, ApprovalID: permission.ApprovalID, Approved: false, Reason: "not now",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(continuation).To(BeNil())
		resolved, err := store.GetSession(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Messages[1].Parts[0].State).To(Equal(session.ToolStateOutputDenied))
		Expect(*resolved.Messages[1].Parts[0].Approval.Approved).To(BeFalse())
		Expect(resolved.Messages[1].Parts[0].Approval.Reason).To(Equal("not now"))
		Expect(resolved.Requests[0].ResolvedAt).NotTo(BeNil())
		Expect(*resolved.Requests[0].ResolvedAt).To(BeTemporally("~", time.Now(), time.Minute))
	})

	It("resumes the provider after the final durable approval without another chat request", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_server_approval_resume"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		store, err := aichat.NewDatabaseThreadStore(db)
		Expect(err).NotTo(HaveOccurred())
		thread, err := store.Create(ctx, "Accounts")
		Expect(err).NotTo(HaveOccurred())
		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())

		provider := &fakeStreamingProvider{backend: api.BackendGemini}
		provider.execute = func(_ context.Context, spec api.Spec) (<-chan api.Event, error) {
			var events []api.Event
			if spec.ToolApproval == nil {
				events = []api.Event{
					{Kind: api.EventToolUse, ToolCallID: "call-account-1", Tool: "accounts_edit", Input: map[string]any{"id": "acc-1"}},
					{Kind: api.EventPermission, ToolCallID: "call-account-1", Tool: "accounts_edit"},
					{Kind: api.EventResult, Success: true, ToolApproval: &api.ToolApprovalState{
						Messages: []api.Message{
							{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Edit the account"}}},
							{Role: api.RoleAssistant, Parts: []api.Part{{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
								ToolCallID: "call-account-1", Name: "accounts_edit", Input: json.RawMessage(`{"id":"acc-1"}`),
							}}}},
						},
						Calls: []api.ToolApprovalCall{{Request: api.ToolApprovalRequest{
							ToolCallID: "call-account-1", Tool: "accounts_edit", Input: json.RawMessage(`{"id":"acc-1"}`),
						}}},
						ProviderCheckpoint: &api.ProviderCheckpoint{
							Codec: "test-provider", Version: 1, Payload: []byte("private provider state"),
						},
					}},
				}
			} else {
				Expect(spec.ToolApproval.Decisions).To(HaveLen(1))
				Expect(spec.ToolApproval.State.ProviderCheckpoint.Payload).To(Equal([]byte("private provider state")))
				events = []api.Event{
					{Kind: api.EventToolUse, ToolCallID: "call-account-1", Tool: "accounts_edit", Input: map[string]any{"id": "acc-1"}},
					{Kind: api.EventToolResult, ToolCallID: "call-account-1", Tool: "accounts_edit", Success: true, Text: `{"updated":true}`},
					{Kind: api.EventText, Text: "Updated."},
					{Kind: api.EventResult, Success: true},
				}
			}
			stream := make(chan api.Event, len(events))
			for _, event := range events {
				stream <- event
			}
			close(stream)
			return stream, nil
		}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider}, Threads: store, Authority: authority,
			Tools: aichat.StaticToolProvider([]api.ToolDefinition{{
				Name: "accounts_edit", DefaultPermission: api.ToolModeAsk,
				Handler: func(context.Context, map[string]any) (any, error) { return nil, nil },
			}}),
		})

		initial := httptest.NewRecorder()
		service.Handler().ServeHTTP(initial, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "gemini", Backend: api.BackendGemini},
			Messages: []aichat.UIMessage{{
				ID: "user-message-1", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Edit the account"}},
			}},
		}))
		Expect(initial.Code).To(Equal(http.StatusOK), initial.Body.String())
		suspended, err := store.GetSession(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(suspended.Requests).To(HaveLen(1))
		Expect(suspended.Requests[0].State).To(Equal("pending"))

		approval := httptest.NewRecorder()
		service.Handler().ServeHTTP(approval, requestJSON(
			http.MethodPost,
			"/api/chat/sessions/"+thread.ID+"/approvals/"+suspended.Requests[0].ID,
			map[string]any{"approved": true},
		))
		Expect(approval.Code).To(Equal(http.StatusOK), approval.Body.String())
		var aggregate session.Session
		Expect(json.Unmarshal(approval.Body.Bytes(), &aggregate)).To(Succeed())
		Expect(provider.specs).To(HaveLen(2))
		Expect(aggregate.Messages).To(HaveLen(2))
		Expect(aggregate.Messages[1].Parts[0].State).To(Equal(session.ToolStateOutputAvailable))
		Expect(aggregate.Messages[1].Parts[0].Output).To(MatchJSON(`{"updated":true}`))
		Expect(aggregate.Messages[1].Parts).NotTo(ContainElement(HaveField("Type", "data-tool-approval")))
		Expect(aggregate.Requests[0].State).To(Equal("approved"))
		Expect(aggregate.Turns).To(HaveLen(1))
		Expect(aggregate.Turns[0].StopReason).To(Equal("stop"))
	})
})
