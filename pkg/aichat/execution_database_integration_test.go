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
		go func() {
			request := mcp.CallToolRequest{}
			request.Params.Name = "account_edit"
			request.Params.Arguments = map[string]any{"name": "Draft"}
			request.Params.Meta = &mcp.Meta{AdditionalFields: map[string]any{"toolUseId": "call-account-1"}}
			result, callErr := client.CallTool(ctx, request)
			outcomes <- callOutcome{result: result, err: callErr}
		}()

		var approval api.Event
		Eventually(execution.Events()).Should(Receive(&approval))
		Expect(approval.Kind).To(Equal(api.EventPermission))
		Expect(approval.ToolCallID).To(Equal("call-account-1"))
		Expect(calls.Load()).To(BeZero())

		Expect(authority.ResolveToolApproval(ctx, aichat.ToolApprovalResolution{
			ThreadID: threadID, ToolCallID: "call-account-1", Approved: true,
			UpdatedInput: map[string]any{"name": "Approved"},
		})).To(Succeed())
		var outcome callOutcome
		Eventually(outcomes).Should(Receive(&outcome))
		Expect(outcome.err).NotTo(HaveOccurred())
		Expect(outcome.result.IsError).To(BeFalse())
		Expect(outcome.result.StructuredContent).To(Equal(map[string]any{"name": "Approved"}))
		Expect(calls.Load()).To(Equal(int32(1)))

		Expect(execution.Observe(ctx, api.Event{
			Kind: api.EventResult, Success: true, SessionID: "provider-session-1",
		})).To(Succeed())
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
