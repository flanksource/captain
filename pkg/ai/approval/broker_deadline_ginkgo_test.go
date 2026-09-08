package approval_test

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// brokerDB is the one migrated Captain database the whole suite shares. Opening
// it is where nearly all of the suite's wall clock goes — a per-container open
// re-runs the migrations and costs several seconds each time — so it is opened
// once at suite scope and handed out by openBrokerDB.
var brokerDB *database.DB

var _ = BeforeSuite(func(ctx SpecContext) {
	handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_approval_broker"})
	opened, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(func() { Expect(opened.Close()).To(Succeed()) })
	brokerDB = opened
})

func openBrokerDB(context.Context) *database.DB {
	GinkgoHelper()
	Expect(brokerDB).NotTo(BeNil(), "the suite's database is opened in BeforeSuite")
	return brokerDB
}

// resolve ends a spec's outstanding broker wait so its goroutine does not
// outlive the spec that started it.
func (r *providerRun) resolve(ctx context.Context, outcomes chan outcome) {
	GinkgoHelper()
	// DeferCleanup runs after the spec's own context is cancelled, and the point
	// of the cleanup is to end a wait that is still live.
	ctx = context.WithoutCancel(ctx)
	Expect(r.db.CancelPendingTurnRequests(ctx, r.session, r.run, "spec finished")).To(Succeed())
	Eventually(outcomes, 5*time.Second).Should(Receive())
}

// An approval window that outlives the run it blocks is a window nobody ever
// reaches: the run dies of its budget first and reports a budget failure, so the
// operator never learns the real cause was a question nobody answered. These
// specs pin the expiry to the earlier of the two.
var _ = Describe("Approval expiry under a run deadline", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		db = openBrokerDB(ctx)
	})

	It("bounds the window by the run's deadline when the deadline is the earlier bound", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(24 * time.Hour)
		deadline := time.Now().Add(10 * time.Minute)
		broker.Deadline = deadline

		outcomes := run.callTool(context.WithoutCancel(ctx), broker, api.PermissionRequest{
			Tool: "Write", Input: map[string]any{"path": "main.go"}, ToolUseID: "toolu_deadline_bound",
		})
		DeferCleanup(func() { run.resolve(ctx, outcomes) })

		event := run.awaitPermission()
		pending, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
		Expect(err).NotTo(HaveOccurred())
		Expect(pending.ExpiresAt).NotTo(BeNil())
		Expect(*pending.ExpiresAt).To(BeTemporally("<", deadline),
			"the approval has to lapse before the run does, or the run reports the wrong cause of death")
		Expect(*pending.ExpiresAt).To(BeTemporally("~", deadline.Add(-approval.DeadlineGrace), time.Second))
	})

	It("keeps the configured window when it is the earlier bound", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(time.Minute)
		broker.Deadline = time.Now().Add(24 * time.Hour)

		outcomes := run.callTool(context.WithoutCancel(ctx), broker, api.PermissionRequest{
			Tool: "Write", Input: map[string]any{"path": "go.mod"}, ToolUseID: "toolu_timeout_bound",
		})
		DeferCleanup(func() { run.resolve(ctx, outcomes) })

		event := run.awaitPermission()
		pending, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
		Expect(err).NotTo(HaveOccurred())
		Expect(*pending.ExpiresAt).To(BeTemporally("~", time.Now().Add(time.Minute), 5*time.Second))
	})

	It("refuses to ask a person a question the run will not live long enough to hear the answer to", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		broker := run.broker(24 * time.Hour)
		broker.Deadline = time.Now().Add(2 * time.Second)

		_, err := broker.CanUseTool(ctx, api.PermissionRequest{
			Tool: "Write", Input: map[string]any{"path": "main.go"}, ToolUseID: "toolu_deadline_passed",
		})
		Expect(err).To(MatchError(ContainSubstring("Write")))
		Expect(err).To(MatchError(ContainSubstring("deadline")))

		requests, listErr := db.ListTurnRequests(ctx, database.TurnRequestFilter{
			SessionID: run.session, PromptRunID: &run.run,
		})
		Expect(listErr).NotTo(HaveOccurred())
		Expect(requests).To(BeEmpty(),
			"a row that can only ever expire is worse than no row: it reads as a live question")
	})

	It("leaves the window unbounded by a deadline the caller did not set", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(context.WithoutCancel(ctx), run.broker(time.Hour), api.PermissionRequest{
			Tool: "Write", Input: map[string]any{"path": "README.md"}, ToolUseID: "toolu_no_deadline",
		})
		DeferCleanup(func() { run.resolve(ctx, outcomes) })

		event := run.awaitPermission()
		pending, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
		Expect(err).NotTo(HaveOccurred())
		Expect(*pending.ExpiresAt).To(BeTemporally("~", time.Now().Add(time.Hour), 5*time.Second))
	})

	// The caller's own context is the tightest deadline there is, and it is the
	// one that produced the observed failure: the run's budget context ended
	// first and the wait returned a bare context error naming no tool.
	It("bounds the window by the calling context's deadline", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		deadline := time.Now().Add(15 * time.Minute)
		callCtx, cancel := context.WithDeadline(context.WithoutCancel(ctx), deadline)
		DeferCleanup(cancel)

		outcomes := run.callTool(callCtx, run.broker(24*time.Hour), api.PermissionRequest{
			Tool: "Write", Input: map[string]any{"path": "main.go"}, ToolUseID: "toolu_ctx_deadline",
		})
		DeferCleanup(func() { run.resolve(ctx, outcomes) })

		event := run.awaitPermission()
		pending, err := db.GetTurnRequest(ctx, uuid.MustParse(event.ApprovalID))
		Expect(err).NotTo(HaveOccurred())
		Expect(*pending.ExpiresAt).To(BeTemporally("~", deadline.Add(-approval.DeadlineGrace), time.Second))
	})
})

// An expiry that says only "tool approval expired" tells an operator nothing
// they can act on: not which tool, not which durable row, not how long a person
// had to answer, not even that a person was asked at all.
var _ = Describe("Approval expiry reporting", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		db = openBrokerDB(ctx)
	})

	It("names the tool, the approval and the wait when nobody answers", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(150*time.Millisecond), api.PermissionRequest{
			Tool: "Write", Input: map[string]any{"path": "main.go"}, ToolUseID: "toolu_expiry_message",
		})
		event := run.awaitPermission()

		var got outcome
		Eventually(outcomes, 2*time.Second).Should(Receive(&got))
		Expect(got.err).To(MatchError(ContainSubstring("expired")))
		Expect(got.err).To(MatchError(ContainSubstring(`"Write"`)), "which tool was being asked about")
		Expect(got.err).To(MatchError(ContainSubstring(event.ApprovalID)), "which durable row to look at")
		Expect(got.err).To(MatchError(MatchRegexp(`waited [0-9]`)), "how long a person had to answer")
		Expect(got.err).To(MatchError(ContainSubstring("nobody answered")), "that a person was asked at all")
	})

	It("narrates the expiry so a reader learns the wait ended", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(150*time.Millisecond), api.PermissionRequest{
			Tool: "Write", Input: map[string]any{"path": "main.go"}, ToolUseID: "toolu_expiry_notify",
		})
		asked := run.awaitPermission()
		Expect(asked.Reason).To(BeEmpty(), "the request frame is the question, not its outcome")

		Eventually(outcomes, 2*time.Second).Should(Receive())

		ended := run.awaitPermission()
		Expect(ended.ApprovalID).To(Equal(asked.ApprovalID))
		Expect(ended.Tool).To(Equal("Write"))
		Expect(ended.Reason).To(ContainSubstring("expired"),
			"a narration that stops at 'awaiting approval' cannot be told apart from a run still waiting")
	})

	It("narrates an approval another writer swept out from under the wait", func(ctx SpecContext) {
		run := newProviderRun(ctx, db)
		outcomes := run.callTool(ctx, run.broker(time.Hour), api.PermissionRequest{
			Tool: "Bash", Input: map[string]any{"command": "rm -rf /"}, ToolUseID: "toolu_swept",
		})
		asked := run.awaitPermission()

		// What the monitor's sweeper does to an approval whose run went away: the
		// row goes terminal without this process deciding anything.
		Expect(db.ExpireToolApprovalRequest(ctx, uuid.MustParse(asked.ApprovalID),
			database.TurnRequestStateCancelled, "prompt run is cancelled")).To(Succeed())

		var got outcome
		Eventually(outcomes, 2*time.Second).Should(Receive(&got))
		Expect(got.err).To(MatchError(ContainSubstring("prompt run is cancelled")))
		ended := run.awaitPermission()
		Expect(ended.Reason).To(ContainSubstring("cancelled"))
	})
})
