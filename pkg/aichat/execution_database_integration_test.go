package aichat_test

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Database execution authority", func() {
	It("blocks an ask tool on its durable approval and revokes the credential at completion", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_execution"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		var calls atomic.Int32
		threadID := uuid.NewString()
		execution, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: threadID, RequestID: "request-account-1", Title: "Accounts",
			Spec: api.Spec{Model: api.Model{
				Name: "sonnet", Backend: api.BackendClaudeAgent,
			}.Capabilities()},
			Definitions: []api.ToolDefinition{{
				Name: "account_edit", DefaultPermission: api.ToolModeAsk,
				Handler: func(_ context.Context, input map[string]any) (any, error) {
					calls.Add(1)
					return input, nil
				},
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(execution.Close)

		client := executionMCPClient(ctx, *execution.CallerTools())
		DeferCleanup(client.Close)
		type callOutcome struct {
			result *mcp.CallToolResult
			err    error
		}
		outcomes := make(chan callOutcome, 1)
		_, err = execution.Observe(ctx, api.Event{
			Kind: api.EventToolUse, Tool: "account_edit", ToolCallID: "call-account-1",
			Input: map[string]any{"name": "Draft"},
		})
		Expect(err).NotTo(HaveOccurred())
		go func() {
			request := mcp.CallToolRequest{}
			request.Params.Name = "account_edit"
			request.Params.Arguments = map[string]any{"name": "Draft"}
			result, callErr := client.CallTool(ctx, request)
			outcomes <- callOutcome{result: result, err: callErr}
		}()

		var approval api.Event
		Eventually(execution.Events()).Should(Receive(&approval))
		Expect(approval.Kind).To(Equal(api.EventPermission))
		Expect(approval.ToolCallID).To(Equal("call-account-1"))
		Expect(approval.ApprovalID).To(MatchRegexp(`^[0-9a-f-]{36}$`))
		Expect(calls.Load()).To(BeZero())

		continuation, err := authority.ResolveToolApproval(ctx, aichat.ToolApprovalResolution{
			ThreadID: threadID, ApprovalID: approval.ApprovalID, Approved: true,
			UpdatedInput: map[string]any{"name": "Approved"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(continuation).To(BeNil())
		var outcome callOutcome
		Eventually(outcomes).Should(Receive(&outcome))
		Expect(outcome.err).NotTo(HaveOccurred())
		Expect(outcome.result.IsError).To(BeFalse())
		Expect(outcome.result.StructuredContent).To(Equal(map[string]any{"name": "Approved"}))
		Expect(calls.Load()).To(Equal(int32(1)))

		_, err = execution.Observe(ctx, api.Event{
			Kind: api.EventResult, Success: true, SessionID: "provider-session-1",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(execution.Close(ctx)).To(Succeed())

		runID := uuid.MustParse(execution.PromptRunID())
		run, err := db.GetPromptRun(ctx, runID)
		Expect(err).NotTo(HaveOccurred())
		Expect(run.State).To(Equal(database.PromptRunStateSucceeded))
		var credential struct {
			RevokedAt *time.Time
		}
		Expect(db.Gorm().WithContext(ctx).
			Table("captain_session_mcp_credentials").
			Select("revoked_at").
			Where("prompt_run_id = ?", runID).
			Scan(&credential).Error).To(Succeed())
		Expect(credential.RevokedAt).NotTo(BeNil())
	})

	It("creates distinct prompt runs for sequential turn identities and rejects a replay", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_sequential_turns"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		threadID := uuid.NewString()
		spec := api.Spec{Model: api.Model{Name: "gemini", Backend: api.BackendGemini}.Capabilities()}
		for _, turnID := range []string{"user-message-1", "user-message-2"} {
			execution, beginErr := authority.Begin(ctx, aichat.ExecutionRequest{
				ThreadID: threadID, RequestID: turnID, Title: "Accounts", Spec: spec,
			})
			Expect(beginErr).NotTo(HaveOccurred())
			_, err = execution.Observe(ctx, api.Event{Kind: api.EventResult, Success: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(execution.Close(ctx)).To(Succeed())
		}

		sessionID := uuid.MustParse(threadID)
		runs, err := db.ListPromptRuns(ctx, database.PromptRunFilter{SessionID: &sessionID})
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(HaveLen(2))
		Expect(runs).To(ConsistOf(
			HaveField("AdmissionKey", "aichat:"+threadID+":user-message-1"),
			HaveField("AdmissionKey", "aichat:"+threadID+":user-message-2"),
		))

		_, err = authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: threadID, RequestID: "user-message-1", Title: "Accounts", Spec: spec,
		})
		Expect(err).To(MatchError(ContainSubstring("already exists in state ended")))
	})

	It("records an interruption and admits a later turn on the same Captain session", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_interrupt_resume"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		threadID := uuid.NewString()
		spec := api.Spec{Model: api.Model{Name: "gpt", Backend: api.BackendOpenAI}.Capabilities()}
		interrupted, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: threadID, RequestID: "user-message-interrupted", Title: "Interrupt", Spec: spec,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(interrupted.Interrupt(ctx, "user")).To(Succeed())
		Expect(interrupted.Close(ctx)).To(Succeed())

		run, err := db.GetPromptRun(ctx, uuid.MustParse(interrupted.PromptRunID()))
		Expect(err).NotTo(HaveOccurred())
		Expect(run.State).To(Equal(database.PromptRunStateCancelled))
		sessionRecord, err := db.GetSession(ctx, uuid.MustParse(threadID))
		Expect(err).NotTo(HaveOccurred())
		Expect(sessionRecord.LifecycleStatus).To(Equal(database.SessionLifecycleInterrupted))
		Expect(sessionRecord.ActivityState).To(Equal(database.SessionActivityIdle))
		turns, err := db.ListThreadTurns(ctx, uuid.MustParse(threadID))
		Expect(err).NotTo(HaveOccurred())
		Expect(turns).To(HaveLen(1))
		Expect(turns[0].Status).To(Equal(string(database.TurnStatusInterrupted)))
		Expect(turns[0].StopReason).NotTo(BeNil())
		Expect(*turns[0].StopReason).To(Equal("interrupt"))
		var modelCall struct{ Status, StopReason string }
		Expect(db.Gorm().WithContext(ctx).Table("captain_model_calls").
			Select("status, stop_reason").Where("prompt_run_id = ?", run.ID).
			Scan(&modelCall).Error).To(Succeed())
		Expect(modelCall.Status).To(Equal(string(database.ModelCallStatusCancelled)))
		Expect(modelCall.StopReason).To(Equal("interrupt"))

		resumed, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: threadID, RequestID: "user-message-resumed", Title: "Interrupt", Spec: spec,
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = resumed.Observe(ctx, api.Event{Kind: api.EventResult, Success: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(resumed.Close(ctx)).To(Succeed())
		sessionRecord, err = db.GetSession(ctx, uuid.MustParse(threadID))
		Expect(err).NotTo(HaveOccurred())
		Expect(sessionRecord.LifecycleStatus).To(Equal(database.SessionLifecycleSucceeded))
		Expect(sessionRecord.ActivityState).To(Equal(database.SessionActivityIdle))
	})

	It("keeps an interrupted API run waiting for its durable approvals", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_waiting_approval"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		execution, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: uuid.NewString(), RequestID: "user-message-approval", Title: "Accounts",
			Spec: api.Spec{
				Model:    api.Model{Name: "gemini", Backend: api.BackendGemini}.Capabilities(),
				Messages: []api.Message{{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Edit the account"}}}},
			},
		})
		Expect(err).NotTo(HaveOccurred())

		permission, err := execution.Observe(ctx, api.Event{
			Kind: api.EventPermission, ToolCallID: "call-account-approval", Tool: "account_edit",
			Input: map[string]any{"id": "acc-1"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(permission.ApprovalID).To(MatchRegexp(`^[0-9a-f-]{36}$`))

		_, err = execution.Observe(ctx, api.Event{
			Kind: api.EventResult, Success: true,
			ToolApproval: &api.ToolApprovalState{
				ProviderCheckpoint: &api.ProviderCheckpoint{
					Codec: "test-checkpoint", Version: 1, Payload: []byte("private provider state"),
				},
				Messages: []api.Message{
					{Role: api.RoleUser, Parts: []api.Part{{Type: api.PartText, Text: "Edit the account"}}},
					{Role: api.RoleAssistant, Parts: []api.Part{{Type: api.PartToolRequest, ToolRequest: &api.ToolRequest{
						ToolCallID: "call-account-approval", Name: "account_edit", Input: []byte(`{"id":"acc-1"}`),
					}}}},
				},
				Calls: []api.ToolApprovalCall{{Request: api.ToolApprovalRequest{
					ToolCallID: "call-account-approval", Tool: "account_edit", Input: []byte(`{"id":"acc-1"}`),
				}}},
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(execution.Close(ctx)).To(Succeed())

		run, err := db.GetPromptRun(ctx, uuid.MustParse(execution.PromptRunID()))
		Expect(err).NotTo(HaveOccurred())
		Expect(run.State).To(Equal(database.PromptRunStateWaiting))
		requests, err := db.ListTurnRequests(ctx, database.TurnRequestFilter{SessionID: run.SessionID, PromptRunID: &run.ID})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests).To(HaveLen(1))
		continuation, err := authority.ResolveToolApproval(ctx, aichat.ToolApprovalResolution{
			ThreadID: run.SessionID.String(), ApprovalID: requests[0].ID.String(), Approved: true,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(continuation).NotTo(BeNil())
		Expect(continuation.Execution.TurnID()).To(Equal(execution.TurnID()))
		Expect(continuation.Spec.ToolApproval).NotTo(BeNil())
		Expect(continuation.Spec.ToolApproval.State.ProviderCheckpoint).NotTo(BeNil())
		Expect(continuation.Spec.ToolApproval.Decisions).To(HaveLen(1))
		Expect(continuation.Spec.ToolApproval.Decisions[0].ApprovalID).To(Equal(requests[0].ID.String()))
		Expect(continuation.Spec.Messages).To(BeEmpty())
		Expect(continuation.Spec.Prompt.User).To(BeEmpty())
		Expect(continuation.Execution.Close(ctx)).To(Succeed())
	})
})

func executionMCPClient(ctx context.Context, endpoint api.CallerToolEndpoint) *mcpclient.Client {
	channel, err := transport.NewStreamableHTTP(endpoint.URL, transport.WithHTTPHeaders(endpoint.Headers))
	Expect(err).NotTo(HaveOccurred())
	client := mcpclient.NewClient(channel)
	Expect(client.Start(ctx)).To(Succeed())
	request := mcp.InitializeRequest{}
	request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	request.Params.ClientInfo = mcp.Implementation{Name: "captain-authority-test", Version: "1.0.0"}
	_, err = client.Initialize(ctx, request)
	Expect(err).NotTo(HaveOccurred())
	return client
}
