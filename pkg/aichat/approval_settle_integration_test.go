package aichat_test

import (
	"context"
	"encoding/json"
	"time"

	"github.com/flanksource/captain/pkg/aichat"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A provider approval is answerable — it is on the session, and its ID has gone
// out on the event stream — from the moment the permission frame is observed.
// The run it blocks only reaches `waiting` once the stream has finished the turn
// and encoded its checkpoint, several statements later. These specs pin what
// happens to an answer that arrives inside that window, which is where a person
// clicking Approve promptly, and the mocked lifecycle suite, both landed.
var _ = Describe("Approvals answered while the suspension is still landing", func() {
	It("waits for the run to park rather than refusing the answer", func(ctx SpecContext) {
		fixture := newApprovalFixture(ctx, "captain_aichat_approval_settles")

		// Nothing has parked the run yet; under the bare store guard this is the
		// exact moment that produced "cannot be resolved before its prompt run is
		// waiting" and a 409.
		suspended := make(chan error, 1)
		go func() {
			time.Sleep(200 * time.Millisecond)
			suspended <- suspendOnAccountsApproval(ctx, fixture.execution)
		}()

		continuation, err := fixture.authority.ResolveToolApproval(ctx, aichat.ToolApprovalResolution{
			ThreadID: fixture.thread.ID, ApprovalID: fixture.approvalID, Approved: false, Reason: "not now",
		})
		Expect(err).NotTo(HaveOccurred(), "an answer that raced the suspension is still a valid answer")
		Expect(continuation).NotTo(BeNil(), "the resolution has to hand back the continuation that resumes the run")
		DeferCleanup(continuation.Execution.Close)
		Expect(<-suspended).To(Succeed())

		resolved, err := fixture.store.GetSession(ctx, fixture.thread.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(resolved.Requests).To(HaveLen(1))
		Expect(resolved.Requests[0].State).To(Equal(string(database.TurnRequestStateDenied)))
		Expect(resolved.Requests[0].Reason).To(Equal("not now"))
	})

	It("refuses an answer once the run it blocks has already ended", func(ctx SpecContext) {
		fixture := newApprovalFixture(ctx, "captain_aichat_approval_run_ended")

		runID, err := uuid.Parse(fixture.execution.PromptRunID())
		Expect(err).NotTo(HaveOccurred())
		run, err := fixture.db.GetPromptRun(ctx, runID)
		Expect(err).NotTo(HaveOccurred())
		cancelled := database.PromptRunStateCancelled
		_, err = fixture.db.UpdatePromptRun(ctx, database.UpdatePromptRunInput{
			ID: run.ID, ExpectedVersion: run.Version, State: &cancelled,
		})
		Expect(err).NotTo(HaveOccurred())

		// No suspension is coming, so this must fail on the run's state rather
		// than burn the whole settle budget waiting for one.
		started := time.Now()
		_, err = fixture.authority.ResolveToolApproval(ctx, aichat.ToolApprovalResolution{
			ThreadID: fixture.thread.ID, ApprovalID: fixture.approvalID, Approved: true,
		})
		Expect(err).To(MatchError(database.ErrTurnRequestConflict))
		Expect(err).To(MatchError(ContainSubstring("already ended (cancelled)")))
		Expect(time.Since(started)).To(BeNumerically("<", time.Second),
			"a run that ended is a decided answer, not something to wait out")
	})
})

type approvalFixture struct {
	db         *database.DB
	store      *aichat.DatabaseThreadStore
	authority  *aichat.DatabaseExecutionAuthority
	thread     *aichat.Thread
	execution  aichat.Execution
	approvalID string
}

// newApprovalFixture drives a chat turn up to the point where the provider has
// asked for permission and the durable approval exists, but the run has not yet
// been parked.
func newApprovalFixture(ctx context.Context, name string) approvalFixture {
	GinkgoHelper()
	testDB := dbtest.ForGinkgo(dbtest.Options{Name: name})
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
		Spec: api.Spec{Model: withCaps(api.Model{Name: "gemini", Mode: api.ModeAPI})},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(store.AppendMessage(ctx, thread.ID, aichat.UIMessage{
		ID: "user-message-1", TurnID: execution.TurnID(), Role: "user",
		Parts: []aichat.UIPart{{Type: "text", Text: "Edit the account"}},
	})).To(Succeed())
	permission, err := execution.Observe(ctx, api.Event{
		Kind: api.EventPermission, ToolCallID: "call-account-1", Tool: "accounts_edit",
		Input: map[string]any{"id": "acc-1"},
	})
	Expect(err).NotTo(HaveOccurred())
	Expect(store.AppendMessage(ctx, thread.ID, aichat.UIMessage{
		ID: execution.TurnID() + "-assistant", TurnID: execution.TurnID(), Role: "assistant",
		Parts: []aichat.UIPart{{
			Type: "dynamic-tool", ToolName: "accounts_edit", ToolCallID: "call-account-1",
			State: "approval-requested", Input: json.RawMessage(`{"id":"acc-1"}`),
			Approval: &aichat.Approval{ID: permission.ApprovalID},
		}},
	})).To(Succeed())
	return approvalFixture{
		db: db, store: store, authority: authority, thread: thread,
		execution: execution, approvalID: permission.ApprovalID,
	}
}

// suspendOnAccountsApproval completes the turn the way a provider that needs an
// approval does: a terminal result carrying the approval state and the private
// checkpoint the resume replays from. This is what parks the run in `waiting`.
func suspendOnAccountsApproval(ctx context.Context, execution aichat.Execution) error {
	_, err := execution.Observe(ctx, api.Event{
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
	return err
}
