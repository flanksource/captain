package approval_test

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// A turn that issues parallel tool calls raises one approval per call, and the
// broker answers each on its own goroutine, so several waits overlap on one
// prompt run. The waiting posture therefore cannot be a bracket around any one
// wait: OnRunning fired from the first wait to finish un-waits a run that other
// waits are still blocking.
//
// That is not cosmetic. ResolveToolApprovalRequest only accepts a
// credential-less approval while its prompt run is waiting, so a run put back
// into `running` with siblings still pending makes those siblings permanently
// unresolvable — the host's approve button returns a conflict and the only
// remaining exit is the expiry.
var _ = Describe("Approvals raised in parallel on one prompt run", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		db = openBrokerDB(ctx)
	})

	It("stays waiting until the last outstanding approval is answered", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)
		detached := context.WithoutCancel(ctx)

		// The two goroutines race, so which wait raised which approval is not
		// knowable from here. Merging the outcomes keeps the spec about the run's
		// posture rather than about an ordering the broker never promises.
		finished := make(chan outcome, 2)
		for _, call := range []api.PermissionRequest{
			{Tool: "Read", Input: map[string]any{"file_path": "a.d.ts"}, ToolUseID: "toolu_parallel_a"},
			{Tool: "Read", Input: map[string]any{"file_path": "b.d.ts"}, ToolUseID: "toolu_parallel_b"},
		} {
			outcomes := run.callTool(detached, broker, call)
			go func() {
				defer GinkgoRecover()
				finished <- <-outcomes
			}()
		}

		// Both waits are live before either is answered, which is the whole point.
		firstEvent := run.awaitPermission()
		secondEvent := run.awaitPermission()
		Expect(firstEvent.ApprovalID).NotTo(Equal(secondEvent.ApprovalID))
		Expect(run.state(ctx)).To(Equal(database.PromptRunStateWaiting))

		_, err := db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
			SessionID: run.session, RequestID: uuid.MustParse(firstEvent.ApprovalID),
			Approved: true, ResolvedBy: "spec",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(finished, 5*time.Second).Should(Receive())

		Consistently(func() database.PromptRunState { return run.state(ctx) }, 500*time.Millisecond).
			To(Equal(database.PromptRunStateWaiting),
				"one approval answered while another is still pending must not un-wait the run")

		// The sibling is still answerable — the symptom of the defect is this
		// resolve failing with a conflict rather than the state being untidy.
		_, err = db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
			SessionID: run.session, RequestID: uuid.MustParse(secondEvent.ApprovalID),
			Approved: true, ResolvedBy: "spec",
		})
		Expect(err).NotTo(HaveOccurred(),
			"a run left running with a pending approval makes that approval unresolvable")
		Eventually(finished, 5*time.Second).Should(Receive())

		Eventually(func() database.PromptRunState { return run.state(ctx) }, 5*time.Second).
			To(Equal(database.PromptRunStateRunning),
				"the last approval to be answered puts the run back to running")
	})

	It("puts a run back to running when its only approval is answered", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)

		outcomes := run.callTool(context.WithoutCancel(ctx), broker, api.PermissionRequest{
			Tool: "Read", Input: map[string]any{"file_path": "solo.d.ts"}, ToolUseID: "toolu_solo",
		})
		event := run.awaitPermission()
		Expect(run.state(ctx)).To(Equal(database.PromptRunStateWaiting))

		_, err := db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
			SessionID: run.session, RequestID: uuid.MustParse(event.ApprovalID),
			Approved: true, ResolvedBy: "spec",
		})
		Expect(err).NotTo(HaveOccurred())
		Eventually(outcomes, 5*time.Second).Should(Receive())

		Eventually(func() database.PromptRunState { return run.state(ctx) }, 5*time.Second).
			To(Equal(database.PromptRunStateRunning))
	})
})
