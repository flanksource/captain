package aichat_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"

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

	It("does not lock an empty thread when provider setup fails", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_failed_provider_setup"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		store, err := aichat.NewDatabaseThreadStore(db)
		Expect(err).NotTo(HaveOccurred())
		thread, err := store.Create(ctx, "Provider setup")
		Expect(err).NotTo(HaveOccurred())
		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{providerErr: errors.New("provider unavailable")},
			Threads:  aichat.FixedThreadStore(store), Authority: authority,
		})

		for index, runtime := range []api.Model{
			{Name: "first-model", Backend: api.BackendOpenAI},
			{Name: "second-model", Backend: api.BackendAnthropic},
		} {
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
				ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message", Runtime: &runtime,
				Messages: []aichat.UIMessage{{
					ID: fmt.Sprintf("failed-user-%d", index), Role: "user",
					Parts: []aichat.UIPart{{Type: "text", Text: "Retry provider setup"}},
				}},
			}))
			Expect(response.Code).To(Equal(http.StatusServiceUnavailable), response.Body.String())
			stored, getErr := store.Get(ctx, thread.ID)
			Expect(getErr).NotTo(HaveOccurred())
			Expect(stored.Messages).To(BeEmpty())
			Expect(stored.Runtime).To(BeNil())
		}
	})

	It("binds and accounts for the provider candidate selected after fallback", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_selected_fallback"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		store, err := aichat.NewDatabaseThreadStore(db)
		Expect(err).NotTo(HaveOccurred())
		thread, err := store.Create(ctx, "Fallback")
		Expect(err).NotTo(HaveOccurred())
		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		provider := &fakeStreamingProvider{
			model: "fallback-model", backend: api.BackendGemini,
			events: []api.Event{
				{Kind: api.EventSystem, SessionID: "fallback-session", Model: "fallback-model"},
				{Kind: api.EventText, Text: "Fallback answer", Model: "fallback-model"},
				{Kind: api.EventResult, Success: true, Model: "fallback-model", CostUSD: 0.25,
					Usage: &api.Usage{InputTokens: 12, OutputTokens: 4}},
			},
		}
		resolver := &fakeResolver{provider: provider}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: resolver, Threads: aichat.FixedThreadStore(store), Authority: authority,
		})
		submit := func(id string, runtime api.Model) *httptest.ResponseRecorder {
			response := httptest.NewRecorder()
			service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
				ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message", Runtime: &runtime,
				Messages: []aichat.UIMessage{{
					ID: id, Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Use the selected runtime"}},
				}},
			}))
			return response
		}

		primary := api.Model{
			Name: "primary-model", Backend: api.BackendOpenAI,
			Fallbacks: []api.Model{{Name: "fallback-model", Backend: api.BackendGemini, Effort: api.EffortHigh}},
		}
		first := submit("fallback-user-1", primary)
		Expect(first.Code).To(Equal(http.StatusOK), first.Body.String())
		stored, err := store.Get(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(stored.Runtime).To(Equal(&api.Model{Name: "fallback-model", Backend: api.BackendGemini}))
		Expect(stored.ProviderSessionID).To(Equal("fallback-session"))

		aggregate, err := store.GetSession(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(aggregate.Model).To(Equal("fallback-model"))
		Expect(aggregate.Backend).To(Equal(string(api.BackendGemini)))
		Expect(aggregate.Usage.InputTokens).To(Equal(12))
		Expect(aggregate.Cost.Total()).To(BeNumerically("~", 0.25, 0.000001))
		sessionID := uuid.MustParse(thread.ID)
		runs, err := db.ListPromptRuns(ctx, database.PromptRunFilter{SessionID: &sessionID})
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(HaveLen(1))
		Expect(runs[0].Runtime.Requested.Model).To(Equal(primary.Name))
		Expect(runs[0].Runtime.Resolved.Model).To(Equal("fallback-model"))
		Expect(runs[0].Runtime.Resolved.Effort).To(Equal(string(api.EffortHigh)))

		conflict := submit("fallback-user-conflict", primary)
		Expect(conflict.Code).To(Equal(http.StatusConflict), conflict.Body.String())
		selected := api.Model{Name: "fallback-model", Backend: api.BackendGemini}
		second := submit("fallback-user-2", selected)
		Expect(second.Code).To(Equal(http.StatusOK), second.Body.String())
		Expect(resolver.configs).To(HaveLen(2))
		Expect(resolver.configs[1].SessionID).To(Equal("fallback-session"))
	})

	It("rejects stale database history snapshots before turn admission or fork creation", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_stale_history"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		store, err := aichat.NewDatabaseThreadStore(db)
		Expect(err).NotTo(HaveOccurred())
		thread, err := store.Create(ctx, "History snapshot")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.AppendMessage(ctx, thread.ID, aichat.UIMessage{
			ID: "first-user", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "First"}},
		})).To(Succeed())
		stale, err := store.Get(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(store.AppendMessage(ctx, thread.ID, aichat.UIMessage{
			ID: "intervening-assistant", Role: "assistant", Parts: []aichat.UIPart{{Type: "text", Text: "Intervening"}},
		})).To(Succeed())

		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		_, err = authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: thread.ID, RequestID: "stale-turn", Title: stale.Title,
			ExpectedThreadUpdatedAt: stale.UpdatedAt,
			Spec:                    api.Spec{Model: api.Model{Name: "test-model", Backend: api.BackendOpenAI}},
		})
		Expect(errors.Is(err, database.ErrSessionConflict)).To(BeTrue())

		_, err = db.ForkChatSession(ctx, database.ForkChatSessionInput{
			SourceSessionID: uuid.MustParse(thread.ID), ExpectedSourceUpdatedAt: stale.UpdatedAt,
			SessionID: uuid.New(), Title: "Stale fork", ProviderMessageID: "stale-seed",
			Role: "user", Parts: json.RawMessage(`[{"type":"text","text":"stale"}]`),
		})
		Expect(errors.Is(err, database.ErrSessionConflict)).To(BeTrue())
	})

	It("persists write-once runtime identity and an independent turnless fork seed", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_runtime_fork"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		store, err := aichat.NewDatabaseThreadStore(db)
		Expect(err).NotTo(HaveOccurred())
		source, err := store.Create(ctx, "Runtime source")
		Expect(err).NotTo(HaveOccurred())
		Expect(store.AppendMessage(ctx, source.ID, aichat.UIMessage{
			ID: "source-user", Role: "user", Parts: []aichat.UIPart{{Type: "text", Text: "Question"}},
		})).To(Succeed())
		Expect(store.AppendMessage(ctx, source.ID, aichat.UIMessage{
			ID: "source-assistant", Role: "assistant", Parts: []aichat.UIPart{{Type: "text", Text: "Answer"}},
		})).To(Succeed())
		runtime := api.Model{Name: "test-model", Backend: api.BackendOpenAI}
		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		execution, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: source.ID, RequestID: "runtime-binding-turn", Title: source.Title,
			Spec: api.Spec{Model: runtime},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(execution.Close(ctx)).To(Succeed())
		unbound, err := store.Get(ctx, source.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(unbound.Runtime).To(BeNil(), "admission alone must not lock a provider runtime")
		Expect(store.SetRuntime(ctx, source.ID, runtime)).To(Succeed())
		bound, err := store.Get(ctx, source.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(bound.Runtime).To(Equal(&runtime))
		Expect(store.SetRuntime(ctx, source.ID, api.Model{
			Name: "test-model", Backend: api.BackendOpenAI, Effort: api.EffortHigh,
		})).To(Succeed())
		err = store.SetRuntime(ctx, source.ID, api.Model{Name: "other-model", Backend: api.BackendOpenAI})
		Expect(errors.Is(err, aichat.ErrThreadRuntimeConflict)).To(BeTrue())
		Expect(store.SetProviderSession(ctx, source.ID, "provider-source")).To(Succeed())
		_, err = store.AddUsage(ctx, source.ID, aichat.TurnUsage{InputTokens: 11, OutputTokens: 5, CostUSD: 0.3})
		Expect(err).NotTo(HaveOccurred())

		fork, err := store.Fork(ctx, source.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(fork.ForkedFrom).To(Equal(source.ID))
		Expect(fork.ProviderSessionID).To(BeEmpty())
		Expect(fork.Runtime).To(BeNil())
		Expect(fork.TotalInputTokens).To(BeZero())
		Expect(fork.TotalCostUSD).To(BeZero())
		Expect(fork.Messages).To(HaveLen(1))
		Expect(fork.Messages[0].TurnID).To(BeEmpty())
		Expect(fork.Messages[0].Parts).To(ContainElement(HaveField("Type", "data-fork-seed")))

		var parentID *uuid.UUID
		var providerSessionID *string
		var metadata string
		Expect(db.Gorm().WithContext(ctx).Raw(`
			SELECT parent_session_id, provider_session_id, metadata::text
			FROM captain_sessions WHERE id = ?
		`, fork.ID).Row().Scan(&parentID, &providerSessionID, &metadata)).To(Succeed())
		Expect(parentID).To(BeNil(), "forks remain independent root sessions")
		Expect(providerSessionID).To(BeNil())
		Expect(metadata).To(ContainSubstring(`"forkedFrom"`))
		Expect(metadata).NotTo(ContainSubstring(`"aichatRuntime"`))

		var seedTurnID *uuid.UUID
		Expect(db.Gorm().WithContext(ctx).Raw(`
			SELECT turn_id FROM captain_messages WHERE session_id = ?
		`, fork.ID).Row().Scan(&seedTurnID)).To(Succeed())
		Expect(seedTurnID).To(BeNil())
		roots, err := store.List(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(roots).To(HaveLen(2))
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
		resolution := aichat.ToolApprovalResolution{
			ThreadID: thread.ID, ApprovalID: permission.ApprovalID, Approved: false, Reason: "not now",
		}
		_, err = authority.ResolveToolApproval(ctx, resolution)
		Expect(err).To(MatchError(ContainSubstring("cannot be resolved before its prompt run is waiting")))
		assistant := aichat.UIMessage{
			ID: execution.TurnID() + "-assistant", TurnID: execution.TurnID(), Role: "assistant",
			Parts: []aichat.UIPart{{
				Type: "dynamic-tool", ToolName: "accounts_edit", ToolCallID: "call-account-1",
				State: "approval-requested", Input: json.RawMessage(`{"id":"acc-1"}`),
				Approval: &aichat.Approval{ID: permission.ApprovalID},
			}},
		}
		Expect(store.AppendMessage(ctx, thread.ID, assistant)).To(Succeed())
		_, err = execution.Observe(ctx, api.Event{
			Kind: api.EventResult, Success: true,
			ToolApproval: &api.ToolApprovalState{
				Messages: []api.Message{{Role: api.RoleAssistant, Parts: []api.Part{{
					Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
						ToolCallID: "call-account-1", Name: "accounts_edit", Input: json.RawMessage(`{"id":"acc-1"}`),
					},
				}}}},
				Calls: []api.ToolApprovalCall{{Request: api.ToolApprovalRequest{
					ToolCallID: "call-account-1", Tool: "accounts_edit", Input: json.RawMessage(`{"id":"acc-1"}`),
				}}},
				ProviderCheckpoint: &api.ProviderCheckpoint{Codec: "test-provider", Version: 1, Payload: []byte("checkpoint")},
			},
		})
		Expect(err).NotTo(HaveOccurred())

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

		continuation, err := authority.ResolveToolApproval(ctx, resolution)
		Expect(err).NotTo(HaveOccurred())
		Expect(continuation).NotTo(BeNil())
		DeferCleanup(continuation.Execution.Close)
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
			Resolver: &fakeResolver{provider: provider}, Threads: aichat.FixedThreadStore(store), Authority: authority,
			Tools: aichat.StaticToolProvider([]api.ToolDefinition{{
				Name: "accounts_edit", DefaultPermission: api.ToolPolicyAsk,
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

	It("terminalizes a tool and preserves an approval persistence failure", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_approval_failure"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		Expect(db.Gorm().WithContext(ctx).Exec(`
			ALTER TABLE captain_turn_requests
			DROP CONSTRAINT captain_turn_requests_tool_approval_identity
		`).Error).To(Succeed())
		Expect(db.Gorm().WithContext(ctx).Exec(`
			ALTER TABLE captain_turn_requests
			ADD CONSTRAINT captain_turn_requests_tool_approval_identity
			CHECK (kind <> 'tool_approval' OR credential_id IS NOT NULL)
		`).Error).To(Succeed())

		store, err := aichat.NewDatabaseThreadStore(db)
		Expect(err).NotTo(HaveOccurred())
		thread, err := store.Create(ctx, "Approval failure")
		Expect(err).NotTo(HaveOccurred())
		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		provider := &fakeStreamingProvider{backend: api.BackendGemini, events: []api.Event{
			{Kind: api.EventToolUse, ToolCallID: "call-account-1", Tool: "accounts_edit", Input: map[string]any{"id": "acc-1"}},
			{Kind: api.EventPermission, ToolCallID: "call-account-1", Tool: "accounts_edit"},
		}}
		service := aichat.NewService(aichat.ServiceOptions{
			Resolver: &fakeResolver{provider: provider}, Threads: aichat.FixedThreadStore(store), Authority: authority,
			Tools: aichat.StaticToolProvider([]api.ToolDefinition{{
				Name: "accounts_edit", DefaultPermission: api.ToolPolicyAsk,
				Handler: func(context.Context, map[string]any) (any, error) {
					Fail("failed approval must not execute its tool")
					return nil, nil
				},
			}}),
		})

		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, requestJSON(http.MethodPost, "/api/chat", aichat.ChatRequest{
			ID: thread.ID, ThreadID: thread.ID, Trigger: "submit-message",
			Runtime: &api.Model{Name: "gemini", Backend: api.BackendGemini},
			Messages: []aichat.UIMessage{{
				ID: "user-approval-failure", Role: "user",
				Parts: []aichat.UIPart{{Type: "text", Text: "Edit the account"}},
			}},
		}))

		Expect(response.Code).To(Equal(http.StatusOK), response.Body.String())
		parts := decodedDataLines(response.Body.String())
		Expect(partTypes(parts)).To(Equal([]string{
			"start", "start-step", "tool-input-available", "tool-output-error",
			"error", "finish-step", "finish",
		}))
		Expect(parts[3]["errorText"]).To(ContainSubstring("captain_turn_requests_tool_approval_identity"), "wire tool error")
		Expect(parts[4]["errorText"]).To(ContainSubstring("captain_turn_requests_tool_approval_identity"), "wire stream error")

		aggregate, err := store.GetSession(ctx, thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(aggregate.LifecycleStatus).To(Equal(string(database.SessionLifecycleFailed)))
		Expect(aggregate.Messages).To(HaveLen(2))
		Expect(aggregate.Messages[1].Parts[0].State).To(Equal(session.ToolStateOutputError))
		Expect(aggregate.Messages[1].Parts[0].ErrorText).To(ContainSubstring("captain_turn_requests_tool_approval_identity"), "persisted tool error")
		Expect(aggregate.Requests).To(BeEmpty())
		Expect(aggregate.Turns).To(HaveLen(1))
		var turnError string
		Expect(db.Gorm().WithContext(ctx).Table("captain_turns").
			Select("error").Where("id = ?", aggregate.Turns[0].ID).
			Row().Scan(&turnError)).To(Succeed())
		Expect(turnError).To(ContainSubstring("captain_turn_requests_tool_approval_identity"), "turn error")

		sessionID := uuid.MustParse(thread.ID)
		runs, err := db.ListPromptRuns(ctx, database.PromptRunFilter{SessionID: &sessionID})
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(HaveLen(1))
		Expect(runs[0].State).To(Equal(database.PromptRunStateFailed))
		Expect(runs[0].Error).To(ContainSubstring("captain_turn_requests_tool_approval_identity"), "prompt run error")
		var modelCallError string
		Expect(db.Gorm().WithContext(ctx).Table("captain_model_calls").
			Select("error").Where("prompt_run_id = ?", runs[0].ID).
			Scan(&modelCallError).Error).To(Succeed())
		Expect(modelCallError).To(ContainSubstring("captain_turn_requests_tool_approval_identity"), "model call error")
	})
})
