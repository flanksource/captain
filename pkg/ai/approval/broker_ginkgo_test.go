package approval_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

// brokerPoll keeps the specs responsive without changing what is exercised: the
// broker re-reads the durable row on this interval instead of the package
// default.
const brokerPoll = 20 * time.Millisecond

type outcome struct {
	decision api.PermissionDecision
	err      error
}

var _ = Describe("Approval broker", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_approval_broker"})
		opened, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(opened.Close()).To(Succeed()) })
		db = opened
	})

	It("blocks on a durable pending row and unblocks with the approved input", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(time.Minute), api.PermissionRequest{
			Tool: "Bash", Input: map[string]any{"command": "ls"}, ToolUseID: "toolu_approve",
		})

		event := run.awaitPermission()
		Expect(event).To(MatchFields(IgnoreExtras, Fields{
			"Tool":       Equal("Bash"),
			"ToolCallID": Equal("toolu_approve"),
			"Input":      Equal(map[string]any{"command": "ls"}),
		}))

		pending, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
		Expect(err).NotTo(HaveOccurred())
		Expect(*pending).To(MatchFields(IgnoreExtras, Fields{
			"State":       Equal(database.TurnRequestStatePending),
			"RequestedBy": Equal("provider"),
			"ToolCallID":  Equal("toolu_approve"),
			"PromptRunID": PointTo(Equal(run.run)),
			"TurnID":      BeNil(),
			"ModelCallID": BeNil(),
			"Request":     Equal(map[string]any{"tool": "Bash", "input": map[string]any{"command": "ls"}}),
		}))
		Consistently(outcomes, 5*brokerPoll).ShouldNot(Receive())

		_, err = db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
			SessionID: run.session, RequestID: pending.ID, Approved: true,
			UpdatedInput: map[string]any{"command": "ls -al"}, ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())

		Eventually(outcomes).Should(Receive(Equal(outcome{decision: api.PermissionDecision{
			Allow: true, UpdatedInput: map[string]any{"command": "ls -al"},
		}})))
		Expect(run.hooks()).To(Equal([2]int{1, 1}))
	})

	It("returns the denial reason as the decision message", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(time.Minute), api.PermissionRequest{
			Tool: "Write", Input: map[string]any{"path": "go.mod"}, ToolUseID: "toolu_deny",
		})

		event := run.awaitPermission()
		_, err := db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
			SessionID: run.session, RequestID: uuid.MustParse(event.ApprovalID),
			Approved: false, Reason: "go.mod is off limits", ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())

		Eventually(outcomes).Should(Receive(Equal(outcome{
			decision: api.PermissionDecision{Message: "go.mod is off limits"},
		})))
	})

	It("reuses one durable row when the same tool call is brokered twice", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		request := api.PermissionRequest{
			Tool: "Edit", Input: map[string]any{"path": "main.go"}, ToolUseID: "toolu_retry",
		}
		first := run.callTool(ctx, run.broker(time.Minute), request)
		firstEvent := run.awaitPermission()
		second := run.callTool(ctx, run.broker(time.Minute), request)
		secondEvent := run.awaitPermission()
		Expect(secondEvent.ApprovalID).To(Equal(firstEvent.ApprovalID))

		requests, err := db.ListTurnRequests(ctx, database.TurnRequestFilter{
			SessionID: run.session, PromptRunID: &run.run,
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests).To(HaveLen(1))

		_, err = db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
			SessionID: run.session, RequestID: requests[0].ID, Approved: true, ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(first).Should(Receive(Equal(outcome{decision: api.PermissionDecision{Allow: true}})))
		Eventually(second).Should(Receive(Equal(outcome{decision: api.PermissionDecision{Allow: true}})))
	})

	It("expires an approval nobody answered before its timeout", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(150*time.Millisecond), api.PermissionRequest{
			Tool: "Bash", Input: map[string]any{"command": "sleep 1"}, ToolUseID: "toolu_expire",
		})
		event := run.awaitPermission()

		var got outcome
		Eventually(outcomes, 2*time.Second).Should(Receive(&got))
		Expect(got.err).To(MatchError(ContainSubstring("expired")))
		expired, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
		Expect(err).NotTo(HaveOccurred())
		Expect(expired.State).To(Equal(database.TurnRequestStateExpired))
	})

	It("cancels the durable row when the calling context ends", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		callCtx, cancel := context.WithCancel(ctx)
		outcomes := run.callTool(callCtx, run.broker(time.Minute), api.PermissionRequest{
			Tool: "Bash", Input: map[string]any{"command": "git push"}, ToolUseID: "toolu_cancel",
		})
		event := run.awaitPermission()
		cancel()

		var got outcome
		Eventually(outcomes).Should(Receive(&got))
		Expect(got.err).To(MatchError(context.Canceled))
		cancelled, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
		Expect(err).NotTo(HaveOccurred())
		Expect(cancelled.State).To(Equal(database.TurnRequestStateCancelled))

		// Resuming is the response to the cancellation, not a casualty of it: the
		// hook runs on a detached context, so the run leaves `waiting` even though
		// the caller's own context is already dead.
		Expect(run.hooks()).To(Equal([2]int{1, 1}))
		Expect(run.state(ctx)).NotTo(Equal(database.PromptRunStateWaiting),
			"a cancelled approval that never resumed leaves the run waiting forever")
	})

	It("resumes the run when the host cannot surface the request", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)
		unreachable := errors.New("event stream closed")
		broker.Notify = func(context.Context, api.Event) error { return unreachable }

		_, err := broker.CanUseTool(ctx, api.PermissionRequest{
			Tool: "Bash", Input: map[string]any{"command": "ls"}, ToolUseID: "toolu_notify_fail",
		})
		Expect(err).To(MatchError(unreachable))
		Expect(run.hooks()).To(Equal([2]int{1, 1}),
			"a run marked waiting for an approval nobody was shown has to be put back")
		Expect(run.state(ctx)).NotTo(Equal(database.PromptRunStateWaiting))
	})

	It("refuses a generated tool-use ID it cannot correlate, before writing any row", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)
		broker.ClaimToolUseID = nil

		_, err := broker.CanUseTool(ctx, api.PermissionRequest{
			Tool: "Bash", Input: map[string]any{"command": "ls"},
			ToolUseID: "local_1", ToolUseIDGenerated: true,
		})
		Expect(err).To(MatchError(approval.ErrInvalidBroker))

		requests, listErr := db.ListTurnRequests(ctx, database.TurnRequestFilter{
			SessionID: run.session, PromptRunID: &run.run,
		})
		Expect(listErr).NotTo(HaveOccurred())
		Expect(requests).To(BeEmpty(),
			"a row keyed on a locally invented ID is one no provider decision could ever match")
		Expect(run.hooks()).To(Equal([2]int{0, 0}))
	})

	It("records the provider's own tool-call ID once the claim resolves it", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)
		var claimed api.PermissionRequest
		broker.ClaimToolUseID = func(_ context.Context, req api.PermissionRequest) (string, error) {
			claimed = req
			return "toolu_provider_1", nil
		}

		outcomes := run.callTool(ctx, broker, api.PermissionRequest{
			Tool: "Bash", Input: map[string]any{"command": "ls"},
			ToolUseID: "local_1", ToolUseIDGenerated: true,
		})
		event := run.awaitPermission()
		Expect(claimed.ToolUseID).To(Equal("local_1"), "the claim is handed the locally generated ID")
		Expect(event.ToolCallID).To(Equal("toolu_provider_1"))

		pending, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
		Expect(err).NotTo(HaveOccurred())
		Expect(pending.ToolCallID).To(Equal("toolu_provider_1"),
			"the durable row is keyed on the ID the provider will send a result for")

		_, err = db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
			SessionID: run.session, RequestID: pending.ID, Approved: true, ResolvedBy: "dashboard",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(outcomes).Should(Receive(Equal(outcome{decision: api.PermissionDecision{Allow: true}})))
	})

	It("brokers a caller-tool approval under its credential, turn and model call", func(ctx SpecContext) {
		run := newCallerToolRun(ctx, db)
		outcomes := run.callTool(ctx, run.callerBroker(), api.PermissionRequest{
			Tool: "Bash", Input: map[string]any{"command": "ls"}, ToolUseID: "toolu_caller",
		})

		event := run.awaitPermission()
		pending, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
		Expect(err).NotTo(HaveOccurred())
		Expect(*pending).To(MatchFields(IgnoreExtras, Fields{
			"RequestedBy":  Equal("caller_tool"),
			"TurnID":       PointTo(Equal(run.turn)),
			"ModelCallID":  PointTo(Equal(run.modelCall)),
			"CredentialID": PointTo(Equal(run.credential)),
		}))

		_, err = db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
			SessionID: run.session, RequestID: pending.ID, Approved: true, ResolvedBy: "chat",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(outcomes).Should(Receive(Equal(outcome{decision: api.PermissionDecision{Allow: true}})))
	})

	It("cancels a caller-tool approval whose credential is revoked mid-wait", func(ctx SpecContext) {
		run := newCallerToolRun(ctx, db)
		outcomes := run.callTool(ctx, run.callerBroker(), api.PermissionRequest{
			Tool: "Bash", Input: map[string]any{"command": "rm -rf /"}, ToolUseID: "toolu_revoked",
		})
		event := run.awaitPermission()

		// The credential is the authority the tool would run under; an approval
		// that outlives it is a decision nobody is still entitled to make.
		Expect(db.RevokeCallerToolCredential(ctx, run.credential, "session ended")).To(Succeed())

		var got outcome
		Eventually(outcomes, 2*time.Second).Should(Receive(&got))
		Expect(got.err).To(MatchError(database.ErrCallerToolCredentialInactive))
		cancelled, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
		Expect(err).NotTo(HaveOccurred())
		Expect(cancelled.State).To(Equal(database.TurnRequestStateCancelled))
	})

	It("accepts a broker that names its database, session, run, notifier and timeout", func() {
		Expect(completeBroker(db).Validate()).To(Succeed())
	})

	DescribeTable("rejects an incomplete broker",
		func(strip func(*approval.Broker), missing string) {
			broker := completeBroker(db)
			strip(broker)
			Expect(broker.Validate()).To(MatchError(ContainSubstring(missing)))
		},
		Entry("without a database", func(b *approval.Broker) { b.DB = nil }, "database"),
		Entry("without a session", func(b *approval.Broker) { b.SessionID = uuid.Nil }, "session"),
		Entry("without a prompt run", func(b *approval.Broker) { b.PromptRunID = uuid.Nil }, "prompt run"),
		Entry("without a notifier", func(b *approval.Broker) { b.Notify = nil }, "notify"),
		Entry("without a timeout", func(b *approval.Broker) { b.Timeout = 0 }, "timeout"),
	)
})

func completeBroker(db *database.DB) *approval.Broker {
	return &approval.Broker{
		DB: db, SessionID: uuid.New(), PromptRunID: uuid.New(), Timeout: time.Minute,
		Notify: func(context.Context, api.Event) error { return nil },
	}
}

// providerRun is a captain session and prompt run with no turn, model call or
// caller-tool credential — the shape `captain prompt run` and an external host
// present to the broker.
type providerRun struct {
	db      *database.DB
	session uuid.UUID
	run     uuid.UUID
	events  chan api.Event

	mu      sync.Mutex
	waiting int
	running int
}

func newProviderRun(ctx context.Context, db *database.DB) *providerRun {
	GinkgoHelper()
	session, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
		ID: uuid.New(), Source: "captain", Provider: "anthropic",
	})
	Expect(err).NotTo(HaveOccurred())
	run, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{SessionID: session.ID})
	Expect(err).NotTo(HaveOccurred())
	return &providerRun{db: db, session: session.ID, run: run.ID, events: make(chan api.Event, 4)}
}

func (r *providerRun) broker(timeout time.Duration) *approval.Broker {
	return &approval.Broker{
		DB: r.db, SessionID: r.session, PromptRunID: r.run, RequestedBy: "provider",
		Timeout: timeout, Poll: brokerPoll, Notify: r.notify,
		OnWaiting: r.markWaiting, OnRunning: r.markRunning,
	}
}

func (r *providerRun) callTool(
	ctx context.Context,
	broker *approval.Broker,
	request api.PermissionRequest,
) chan outcome {
	outcomes := make(chan outcome, 1)
	go func() {
		defer GinkgoRecover()
		decision, err := broker.CanUseTool(ctx, request)
		outcomes <- outcome{decision: decision, err: err}
	}()
	return outcomes
}

func (r *providerRun) awaitPermission() api.Event {
	GinkgoHelper()
	var event api.Event
	Eventually(r.events).Should(Receive(&event))
	Expect(event.Kind).To(Equal(api.EventPermission))
	Expect(event.ApprovalID).NotTo(BeEmpty())
	return event
}

func (r *providerRun) notify(ctx context.Context, event api.Event) error {
	select {
	case r.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// markWaiting is the host callback a credential-less approval depends on:
// ResolveToolApprovalRequest only accepts one while its prompt run is waiting.
func (r *providerRun) markWaiting(ctx context.Context) error {
	r.mu.Lock()
	r.waiting++
	r.mu.Unlock()
	return r.setState(ctx, database.PromptRunStateWaiting)
}

func (r *providerRun) markRunning(ctx context.Context) error {
	r.mu.Lock()
	r.running++
	r.mu.Unlock()
	return r.setState(ctx, database.PromptRunStateRunning)
}

func (r *providerRun) setState(ctx context.Context, state database.PromptRunState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	current, err := r.db.GetPromptRun(ctx, r.run)
	if err != nil {
		return err
	}
	_, err = r.db.UpdatePromptRun(ctx, database.UpdatePromptRunInput{
		ID: current.ID, ExpectedVersion: current.Version, State: &state,
	})
	return err
}

// state is the prompt run's durable state, which is what a host reading the
// dashboard sees: a run still parked in `waiting` after its approval ended is
// the symptom every unpaired OnWaiting produces.
func (r *providerRun) state(ctx context.Context) database.PromptRunState {
	GinkgoHelper()
	current, err := r.db.GetPromptRun(ctx, r.run)
	Expect(err).NotTo(HaveOccurred())
	return current.State
}

func (r *providerRun) hooks() [2]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return [2]int{r.waiting, r.running}
}
