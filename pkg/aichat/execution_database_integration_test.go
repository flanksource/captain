package aichat_test

import (
	"context"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/gitagent"
	"github.com/flanksource/clicky/text"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"gorm.io/gorm"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
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
				Name: "account_edit", DefaultPermission: api.ToolPolicyAsk,
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

	It("approves and executes an authenticated delegated tool without a local provider tool-use event", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_delegated_execution"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		var calls atomic.Int32
		threadID := uuid.NewString()
		execution, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: threadID, RequestID: "request-delegated-1", Title: "Delegated",
			Spec: api.Spec{Model: api.Model{
				Name: "codex", Backend: api.BackendCodexAgent,
			}.Capabilities()},
			Definitions: []api.ToolDefinition{{
				Name: "version", DefaultPermission: api.ToolPolicyAsk,
				Handler: func(context.Context, map[string]any) (any, error) {
					calls.Add(1)
					return map[string]any{"version": "test"}, nil
				},
			}},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(execution.Close)
		supervisor := httptest.NewTLSServer(callertools.RemoteHandler())
		DeferCleanup(supervisor.Close)
		endpoint := execution.CallerTools()
		Expect(endpoint).NotTo(BeNil())
		delegation, err := endpoint.Delegate(ctx, api.CallerToolBinding{
			TaskID: "task-delegated-1", Agent: "agent-1", ExpiresAt: time.Now().Add(time.Minute),
			ToolNames: []string{"version"},
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(delegation.Revoke)

		sidecarRoot := GinkgoT().TempDir()
		sidecar := httptest.NewUnstartedServer(nil)
		sidecarHandler, err := gitagent.NewCallerToolProxy(gitagent.CallerToolProxyConfig{
			Root: sidecarRoot, EndpointURL: "https://" + sidecar.Listener.Addr().String(),
			SupervisorURL: supervisor.URL, SupervisorCAPath: testServerCAPath(supervisor),
			Agent: "agent-1", DefaultRunner: true,
			IdentifySupervisor: func(request *http.Request) (string, error) {
				if request.Header.Get("Authorization") != "Bearer dispatch-token" {
					return "", errors.New("invalid supervisor credential")
				}
				return "supervisor", nil
			},
		})
		Expect(err).NotTo(HaveOccurred())
		sidecar.Config.Handler = sidecarHandler
		sidecar.StartTLS()
		DeferCleanup(sidecar.Close)
		target := gitagent.TransportTarget{
			URL: sidecar.URL, Token: text.NewSensitiveString("dispatch-token"),
			CAPath: testServerCAPath(sidecar),
		}
		Expect(gitagent.RegisterCallerTools(ctx, target, gitagent.CallerToolGrant{
			Task: "task-delegated-1", Agent: "agent-1", Endpoint: delegation.Endpoint,
			ExpiresAt: time.Now().Add(time.Minute),
		})).To(Succeed())
		remoteEndpoint, err := gitagent.LoadCallerToolEndpoint(sidecarRoot, "task-delegated-1")
		Expect(err).NotTo(HaveOccurred())
		client := executionMCPClientWithHTTP(ctx, *remoteEndpoint, sidecar.Client())
		DeferCleanup(client.Close)
		type callOutcome struct {
			result *mcp.CallToolResult
			err    error
		}
		outcomes := make(chan callOutcome, 1)
		go func() {
			request := mcp.CallToolRequest{}
			request.Params.Name = "version"
			result, callErr := client.CallTool(ctx, request)
			outcomes <- callOutcome{result: result, err: callErr}
		}()

		var use api.Event
		Eventually(execution.Events()).Should(Receive(&use))
		Expect(use).To(MatchFields(IgnoreExtras, Fields{
			"Kind": Equal(api.EventToolUse), "Tool": Equal("version"),
		}))
		Expect(use.ToolCallID).To(HavePrefix("mcp_"))
		var approval api.Event
		Eventually(execution.Events()).Should(Receive(&approval))
		Expect(approval).To(MatchFields(IgnoreExtras, Fields{
			"Kind": Equal(api.EventPermission), "Tool": Equal("version"),
			"ToolCallID": Equal(use.ToolCallID),
		}))
		Expect(calls.Load()).To(BeZero())
		continuation, err := authority.ResolveToolApproval(ctx, aichat.ToolApprovalResolution{
			ThreadID: threadID, ApprovalID: approval.ApprovalID, Approved: true,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(continuation).To(BeNil())
		var terminal api.Event
		Eventually(execution.Events()).Should(Receive(&terminal))
		Expect(terminal).To(MatchFields(IgnoreExtras, Fields{
			"Kind": Equal(api.EventToolResult), "Tool": Equal("version"),
			"ToolCallID": Equal(use.ToolCallID), "Success": BeTrue(),
		}))
		var outcome callOutcome
		Eventually(outcomes).Should(Receive(&outcome))
		Expect(outcome.err).NotTo(HaveOccurred())
		Expect(outcome.result.IsError).To(BeFalse())
		Expect(outcome.result.StructuredContent).To(Equal(map[string]any{"version": "test"}))
		Expect(calls.Load()).To(Equal(int32(1)))

		go func() {
			request := mcp.CallToolRequest{}
			request.Params.Name = "version"
			result, callErr := client.CallTool(ctx, request)
			outcomes <- callOutcome{result: result, err: callErr}
		}()
		var deniedUse, deniedApproval api.Event
		Eventually(execution.Events()).Should(Receive(&deniedUse))
		Eventually(execution.Events()).Should(Receive(&deniedApproval))
		Expect(deniedUse.Kind).To(Equal(api.EventToolUse))
		Expect(deniedApproval).To(MatchFields(IgnoreExtras, Fields{
			"Kind": Equal(api.EventPermission), "ToolCallID": Equal(deniedUse.ToolCallID),
		}))
		continuation, err = authority.ResolveToolApproval(ctx, aichat.ToolApprovalResolution{
			ThreadID: threadID, ApprovalID: deniedApproval.ApprovalID, Approved: false,
			Reason: "operator denied remote version",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(continuation).To(BeNil())
		Eventually(execution.Events()).Should(Receive(&terminal))
		Expect(terminal).To(MatchFields(IgnoreExtras, Fields{
			"Kind": Equal(api.EventToolResult), "ToolCallID": Equal(deniedUse.ToolCallID),
			"Success": BeFalse(), "Text": Equal("operator denied remote version"),
		}))
		Eventually(outcomes).Should(Receive(&outcome))
		Expect(outcome.err).NotTo(HaveOccurred())
		Expect(outcome.result.IsError).To(BeTrue())
		Expect(outcome.result.Content).NotTo(BeEmpty())
		text, ok := mcp.AsTextContent(outcome.result.Content[0])
		Expect(ok).To(BeTrue())
		Expect(text.Text).To(Equal("operator denied remote version"))
		Expect(calls.Load()).To(Equal(int32(1)))

		_, err = execution.Observe(ctx, api.Event{
			Kind: api.EventResult, Success: true, SessionID: "remote-provider-session-1",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(execution.Close(ctx)).To(Succeed())
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

	It("rolls back an admission when its model call cannot be created", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_atomic_admission"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		threadID := uuid.NewString()
		_, err = authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: threadID, RequestID: "invalid-model-call", Title: "Atomic admission",
			Spec: api.Spec{Model: api.Model{Backend: api.BackendOpenAI}.Capabilities()},
		})
		Expect(err).To(MatchError(ContainSubstring("model")))

		sessionID := uuid.MustParse(threadID)
		turns, err := db.ListThreadTurns(ctx, sessionID)
		Expect(err).NotTo(HaveOccurred())
		Expect(turns).To(BeEmpty())
		runs, err := db.ListPromptRuns(ctx, database.PromptRunFilter{SessionID: &sessionID})
		Expect(err).NotTo(HaveOccurred())
		Expect(runs).To(BeEmpty())

		execution, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: threadID, RequestID: "valid-model-call", Title: "Atomic admission",
			Spec: api.Spec{Model: api.Model{Name: "gpt", Backend: api.BackendOpenAI}.Capabilities()},
		})
		Expect(err).NotTo(HaveOccurred())
		_, err = execution.Observe(ctx, api.Event{Kind: api.EventResult, Success: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(execution.Close(ctx)).To(Succeed())
	})

	It("terminalizes an incomplete admission before opening the next turn", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_incomplete_admission"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		session, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ID: uuid.New(), Source: "aichat", Provider: "openai", HostID: "local",
		})
		Expect(err).NotTo(HaveOccurred())
		incompleteTurn, created, err := db.CreateChatTurn(ctx, database.CreateChatTurnInput{
			SessionID: session.ID, ProviderTurnID: "incomplete-model-call",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		incompleteRun, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{
			SessionID: session.ID, TurnID: &incompleteTurn.ID,
			AdmissionKey: "aichat:" + session.ID.String() + ":incomplete-model-call",
		})
		Expect(err).NotTo(HaveOccurred())

		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		execution, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: session.ID.String(), RequestID: "valid-model-call", Title: "Recovered admission",
			Spec: api.Spec{Model: api.Model{Name: "gpt", Backend: api.BackendOpenAI}.Capabilities()},
		})
		Expect(err).NotTo(HaveOccurred())

		incompleteTurn, err = db.GetChatTurn(ctx, incompleteTurn.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(incompleteTurn.Status).To(Equal(database.TurnStatusError))
		incompleteRun, err = db.GetPromptRun(ctx, incompleteRun.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(incompleteRun.State).To(Equal(database.PromptRunStateFailed))
		Expect(incompleteRun.Error).To(Equal("chat execution admission did not complete"))

		_, err = execution.Observe(ctx, api.Event{Kind: api.EventResult, Success: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(execution.Close(ctx)).To(Succeed())
	})

	It("resumes an incomplete admission when the same request is retried", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_incomplete_retry"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		requestID := "retried-model-call"
		session, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ID: uuid.New(), Source: "aichat", Provider: "openai", HostID: "local",
		})
		Expect(err).NotTo(HaveOccurred())
		incompleteTurn, created, err := db.CreateChatTurn(ctx, database.CreateChatTurnInput{
			SessionID: session.ID, ProviderTurnID: requestID,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeTrue())
		incompleteRun, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{
			SessionID: session.ID, TurnID: &incompleteTurn.ID,
			AdmissionKey: "aichat:" + session.ID.String() + ":" + requestID,
		})
		Expect(err).NotTo(HaveOccurred())

		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		execution, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: session.ID.String(), RequestID: requestID, Title: "Retried admission",
			Spec: api.Spec{Model: api.Model{Name: "gpt", Backend: api.BackendOpenAI}.Capabilities()},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(execution.TurnID()).To(Equal(incompleteTurn.ID.String()))
		Expect(execution.PromptRunID()).To(Equal(incompleteRun.ID.String()))

		_, err = execution.Observe(ctx, api.Event{Kind: api.EventResult, Success: true})
		Expect(err).NotTo(HaveOccurred())
		Expect(execution.Close(ctx)).To(Succeed())
		turns, err := db.ListThreadTurns(ctx, session.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(turns).To(HaveLen(1))
		Expect(turns[0].Status).To(Equal(string(database.TurnStatusEnded)))
	})

	It("rejects a second admission while the first turn is running", func(ctx SpecContext) {
		testDB := dbtest.ForGinkgo(dbtest.Options{Name: "captain_aichat_active_admission"})
		db, err := database.Open(ctx, database.WithDSN(testDB.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)

		authority, err := aichat.NewDatabaseExecutionAuthority(db)
		Expect(err).NotTo(HaveOccurred())
		threadID := uuid.NewString()
		spec := api.Spec{Model: api.Model{Name: "gpt", Backend: api.BackendOpenAI}.Capabilities()}
		execution, err := authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: threadID, RequestID: "running-model-call", Title: "Active admission", Spec: spec,
		})
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(execution.Close)

		_, err = authority.Begin(ctx, aichat.ExecutionRequest{
			ThreadID: threadID, RequestID: "second-model-call", Title: "Active admission", Spec: spec,
		})
		Expect(errors.Is(err, database.ErrOpenChatTurn)).To(BeTrue())
		Expect(err.Error()).NotTo(ContainSubstring("duplicate key"))
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

	It("keeps an approval pending when its prompt run changes before the claim", func(ctx SpecContext) {
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

		versionRead := make(chan struct{})
		continueResolution := make(chan struct{})
		var intercepted atomic.Bool
		const callback = "test:pause_approval_after_prompt_run_read"
		Expect(db.Gorm().Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
			if tx.Statement.Table == "captain_prompt_runs" && intercepted.CompareAndSwap(false, true) {
				close(versionRead)
				<-continueResolution
			}
		})).To(Succeed())
		DeferCleanup(func() {
			Expect(db.Gorm().Callback().Query().Remove(callback)).To(Succeed())
		})
		DeferCleanup(func() {
			select {
			case <-continueResolution:
			default:
				close(continueResolution)
			}
		})
		type resolutionResult struct {
			continuation *aichat.ApprovalContinuation
			err          error
		}
		result := make(chan resolutionResult, 1)
		go func() {
			continuation, resolveErr := authority.ResolveToolApproval(ctx, aichat.ToolApprovalResolution{
				ThreadID: run.SessionID.String(), ApprovalID: requests[0].ID.String(), Approved: true,
			})
			result <- resolutionResult{continuation: continuation, err: resolveErr}
		}()
		Eventually(versionRead).Should(BeClosed())
		Expect(db.Transaction(ctx, func(tx *database.DB) error {
			if setErr := tx.Gorm().Exec("SET LOCAL captain.suppress_session_change = 'on'").Error; setErr != nil {
				return setErr
			}
			return tx.Gorm().Exec(`
				UPDATE captain_prompt_runs
				SET current_iteration = current_iteration + 1
				WHERE id = ?
			`, run.ID).Error
		})).To(Succeed())
		close(continueResolution)
		var conflict resolutionResult
		Eventually(result).Should(Receive(&conflict))
		Expect(errors.Is(conflict.err, database.ErrPromptRunConflict)).To(BeTrue())
		Expect(conflict.continuation).To(BeNil())
		pending, err := db.GetTurnRequest(ctx, requests[0].ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(pending.State).To(Equal(database.TurnRequestStatePending))

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
	return executionMCPClientWithHTTP(ctx, endpoint, nil)
}

func executionMCPClientWithHTTP(ctx context.Context, endpoint api.CallerToolEndpoint, httpClient *http.Client) *mcpclient.Client {
	options := []transport.StreamableHTTPCOption{transport.WithHTTPHeaders(endpoint.Headers)}
	if httpClient != nil {
		options = append(options, transport.WithHTTPBasicClient(httpClient))
	}
	channel, err := transport.NewStreamableHTTP(endpoint.URL, options...)
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

func testServerCAPath(server *httptest.Server) string {
	certificate := server.Certificate()
	Expect(certificate).NotTo(BeNil())
	payload := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	path := filepath.Join(GinkgoT().TempDir(), "ca.pem")
	Expect(os.WriteFile(path, payload, 0o600)).To(Succeed())
	return path
}
