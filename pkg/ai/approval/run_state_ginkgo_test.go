package approval_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

// The broker owns the prompt run's waiting posture: the store only resolves a
// credential-less approval while its run is waiting, so a host that had to write
// that state itself could not broker an approval without writing Captain's
// tables. These specs pin every way into and out of the wait to the run row.
var _ = Describe("Prompt run posture around an approval", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		db = openBrokerDB(ctx)
	})

	It("holds the run in waiting while the approval is pending and releases it once answered", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(time.Minute), api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Bash", Input: map[string]any{"command": "ls"}, ToolUseID: "toolu_posture_answer",
		})
		event := run.awaitPermission()
		Expect(run.state(ctx)).To(Equal(database.PromptRunStateWaiting))

		_, err := approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: uuid.MustParse(event.ApprovalID), Approved: true, ResolvedBy: "spec",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(outcomes).Should(Receive(Equal(outcome{decision: api.ApprovalDecision{Allow: true}})))
		Expect(run.state(ctx)).To(Equal(database.PromptRunStateRunning))
	})

	It("releases the run when the approval expires unanswered", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(150*time.Millisecond), api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Bash", Input: map[string]any{"command": "sleep 1"}, ToolUseID: "toolu_posture_expire",
		})
		run.awaitPermission()
		Expect(run.state(ctx)).To(Equal(database.PromptRunStateWaiting))

		var got outcome
		Eventually(outcomes, 2*time.Second).Should(Receive(&got))
		Expect(got.err).To(MatchError(ContainSubstring("expired")))
		Expect(run.state(ctx)).To(Equal(database.PromptRunStateRunning))
	})

	It("hands the host each row it wrote, so a cached copy can adopt the new version", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)
		var mu sync.Mutex
		var seen []database.PromptRun
		broker.OnRunState = func(_ context.Context, written *database.PromptRun) error {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, *written)
			return nil
		}
		outcomes := run.callTool(ctx, broker, api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Bash", Input: map[string]any{"command": "ls"}, ToolUseID: "toolu_posture_hook",
		})
		event := run.awaitPermission()
		_, err := approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: uuid.MustParse(event.ApprovalID), Approved: true, ResolvedBy: "spec",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(outcomes).Should(Receive())

		final, err := db.GetPromptRun(ctx, run.run)
		Expect(err).NotTo(HaveOccurred())
		mu.Lock()
		defer mu.Unlock()
		Expect(seen).To(HaveExactElements(
			MatchFields(IgnoreExtras, Fields{"ID": Equal(run.run), "State": Equal(database.PromptRunStateWaiting)}),
			MatchFields(IgnoreExtras, Fields{"ID": Equal(run.run), "State": Equal(database.PromptRunStateRunning), "Version": Equal(final.Version)}),
		))
		Expect(seen[1].Version).To(BeNumerically(">", seen[0].Version))
	})

	It("refuses to raise an approval on a run that already ended, and cancels the row it recorded", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		run.setState(ctx, database.PromptRunStateCancelled)

		_, err := run.broker(time.Minute).OnApproval(ctx, api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Bash", Input: map[string]any{"command": "ls"}, ToolUseID: "toolu_posture_ended",
		})
		Expect(err).To(MatchError(database.ErrPromptRunConflict))
		Expect(err).To(MatchError(ContainSubstring("cancelled")))

		requests, listErr := db.ListTurnRequests(ctx, database.TurnRequestFilter{SessionID: run.session, PromptRunID: &run.run})
		Expect(listErr).NotTo(HaveOccurred())
		Expect(requests).To(HaveExactElements(MatchFields(IgnoreExtras, Fields{
			"State": Equal(database.TurnRequestStateCancelled),
		})), "a question on a finished run is one nobody can answer")
		Expect(run.state(ctx)).To(Equal(database.PromptRunStateCancelled))
	})

	It("never resurrects a run that was stopped while its approval was pending", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(time.Minute), api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Bash", Input: map[string]any{"command": "git push"}, ToolUseID: "toolu_posture_stop",
		})
		run.awaitPermission()

		run.setState(ctx, database.PromptRunStateCancelled)
		Expect(approval.CancelPending(ctx, db, run.session, run.run, "run stopped")).To(Succeed())

		var got outcome
		Eventually(outcomes, 2*time.Second).Should(Receive(&got))
		Expect(got.err).To(MatchError(ContainSubstring("run stopped")))
		Expect(run.state(ctx)).To(Equal(database.PromptRunStateCancelled))
	})

	It("reports a host hook that fails rather than dropping it", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)
		refused := errors.New("session activity write failed")
		broker.OnRunState = func(context.Context, *database.PromptRun) error { return refused }

		_, err := broker.OnApproval(ctx, api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Bash", Input: map[string]any{"command": "ls"}, ToolUseID: "toolu_posture_hook_fail",
		})
		Expect(err).To(MatchError(refused))
		Expect(run.state(ctx)).To(Equal(database.PromptRunStateRunning),
			"a wait that never started still has to put the run back")
	})
})

var _ = Describe("Resolving an approval", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		db = openBrokerDB(ctx)
	})

	It("records the decision and resolves the session from the row when none is named", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(time.Minute), api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Edit", Input: map[string]any{"path": "main.go"}, ToolUseID: "toolu_resolve_input",
		})
		event := run.awaitPermission()

		resolved, err := approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: uuid.MustParse(event.ApprovalID), Approved: true, ResolvedBy: "dashboard",
			Reason: "  looks right  ", UpdatedInput: map[string]any{"path": "cmd/main.go"},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(*resolved).To(MatchFields(IgnoreExtras, Fields{
			"SessionID":  Equal(run.session),
			"State":      Equal(database.TurnRequestStateApproved),
			"ResolvedBy": Equal("dashboard"),
			"Reason":     Equal("looks right"),
			"Response":   Equal(map[string]any{"updatedInput": map[string]any{"path": "cmd/main.go"}}),
		}))
		Eventually(outcomes).Should(Receive(Equal(outcome{decision: api.ApprovalDecision{
			Allow: true, UpdatedInput: map[string]any{"path": "cmd/main.go"},
		}})))
	})

	It("denies with the reason fed back to the agent", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(time.Minute), api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Write", Input: map[string]any{"path": "go.mod"}, ToolUseID: "toolu_resolve_deny",
		})
		event := run.awaitPermission()

		_, err := approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: uuid.MustParse(event.ApprovalID), SessionID: run.session,
			ResolvedBy: "dashboard", Reason: "go.mod is off limits",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(outcomes).Should(Receive(Equal(outcome{
			decision: api.ApprovalDecision{Message: "go.mod is off limits"},
		})))
	})

	It("refuses a request that belongs to another session", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(time.Minute), api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Bash", Input: map[string]any{"command": "ls"}, ToolUseID: "toolu_resolve_foreign",
		})
		event := run.awaitPermission()
		DeferCleanup(run.resolve, outcomes)

		_, err := approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: uuid.MustParse(event.ApprovalID), SessionID: uuid.New(), Approved: true, ResolvedBy: "spec",
		})
		Expect(err).To(MatchError(database.ErrTurnRequestNotFound))
	})

	DescribeTable("rejects a decision that cannot be recorded",
		func(ctx SpecContext, input approval.ResolveInput, want error, detail string) {
			_, err := approval.Resolve(ctx, db, input)
			Expect(err).To(MatchError(want))
			Expect(err).To(MatchError(ContainSubstring(detail)))
		},
		Entry("without a request", approval.ResolveInput{Approved: true, ResolvedBy: "spec"},
			approval.ErrInvalidResolution, "request ID"),
		Entry("without who resolved it", approval.ResolveInput{RequestID: uuid.New(), Approved: true},
			approval.ErrInvalidResolution, "resolved by"),
		Entry("a denial that also replaces the input", approval.ResolveInput{
			RequestID: uuid.New(), ResolvedBy: "spec", UpdatedInput: map[string]any{"command": "ls"},
		}, approval.ErrInvalidResolution, "denied"),
		Entry("an unknown request", approval.ResolveInput{RequestID: uuid.New(), Approved: true, ResolvedBy: "spec"},
			database.ErrTurnRequestNotFound, "not found"),
	)
})

var _ = Describe("Cancelling a run's pending approvals", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		db = openBrokerDB(ctx)
	})

	It("cancels every pending approval on the run, ends each wait, and keeps answered ones", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)
		answered := run.callTool(ctx, broker, api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Read", Input: map[string]any{"file_path": "a.go"}, ToolUseID: "toolu_cancel_answered",
		})
		answeredEvent := run.awaitPermission()
		_, err := approval.Resolve(ctx, db, approval.ResolveInput{
			RequestID: uuid.MustParse(answeredEvent.ApprovalID), Approved: true, ResolvedBy: "spec",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(answered).Should(Receive())

		first := run.callTool(ctx, broker, api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Bash", Input: map[string]any{"command": "make"}, ToolUseID: "toolu_cancel_a",
		})
		run.awaitPermission()
		second := run.callTool(ctx, broker, api.ApprovalRequest{
			Kind: api.ApprovalKindTool,
			Tool: "Bash", Input: map[string]any{"command": "make test"}, ToolUseID: "toolu_cancel_b",
		})
		run.awaitPermission()

		Expect(approval.CancelPending(ctx, db, run.session, run.run, "run stopped")).To(Succeed())

		for _, outcomes := range []chan outcome{first, second} {
			var got outcome
			Eventually(outcomes, 2*time.Second).Should(Receive(&got))
			Expect(got.err).To(MatchError(ContainSubstring("run stopped")))
		}
		requests, err := db.ListTurnRequests(ctx, database.TurnRequestFilter{SessionID: run.session, PromptRunID: &run.run})
		Expect(err).NotTo(HaveOccurred())
		Expect(requests).To(ConsistOf(
			MatchFields(IgnoreExtras, Fields{"ToolCallID": Equal("toolu_cancel_answered"), "State": Equal(database.TurnRequestStateApproved)}),
			MatchFields(IgnoreExtras, Fields{"ToolCallID": Equal("toolu_cancel_a"), "State": Equal(database.TurnRequestStateCancelled), "Reason": Equal("run stopped")}),
			MatchFields(IgnoreExtras, Fields{"ToolCallID": Equal("toolu_cancel_b"), "State": Equal(database.TurnRequestStateCancelled), "Reason": Equal("run stopped")}),
		))
		Eventually(func() database.PromptRunState { return run.state(ctx) }, 2*time.Second).
			Should(Equal(database.PromptRunStateRunning), "the last wait to end releases the run")
	})

	It("requires the session and prompt run it cancels", func(ctx SpecContext) {
		Expect(approval.CancelPending(ctx, db, uuid.Nil, uuid.New(), "stop")).To(MatchError(database.ErrTurnRequestInvalid))
		Expect(approval.CancelPending(ctx, db, uuid.New(), uuid.Nil, "stop")).To(MatchError(database.ErrTurnRequestInvalid))
	})
})
