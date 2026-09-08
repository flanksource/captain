package monitor

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// Nothing outside the broker's own wait loop ever enforced expires_at, so an
// approval outlived the process that raised it: `gavel serve` restarts, the
// laptop sleeps, the agent crashes, and the row stays `pending` for good with a
// timestamp nobody reads. The monitor is where the sweep belongs — it already
// holds the single-writer lock and already runs on a ticker.
var _ = Describe("Tool approval sweep", Ordered, func() {
	var db *database.DB

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_monitor_sweep"})
		opened, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(opened.Close()).To(Succeed()) })
		db = opened
	})

	It("expires an approval whose window closed with nobody having answered", func(ctx SpecContext) {
		run := newSweepRun(ctx, db)
		lapsed := run.approval(ctx, "toolu_sweep_lapsed", "Write", time.Now().Add(50*time.Millisecond))

		Eventually(func(g Gomega) database.TurnRequestState {
			result, err := sweepToolApprovals(ctx, db, time.Now().UTC())
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(result.failed).To(BeZero())
			return run.state(ctx, lapsed.ID)
		}, 2*time.Second).Should(Equal(database.TurnRequestStateExpired))

		swept, err := db.GetTurnRequest(ctx, lapsed.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(swept.Reason).To(ContainSubstring("Write"), "the row has to say which question went unanswered")
		Expect(swept.ResolvedAt).NotTo(BeNil())
	})

	It("cancels an approval whose prompt run is already over", func(ctx SpecContext) {
		run := newSweepRun(ctx, db)
		orphan := run.approval(ctx, "toolu_sweep_orphan", "Bash", time.Now().Add(time.Hour))
		run.setRunState(ctx, database.PromptRunStateFailed)

		result, err := sweepToolApprovals(ctx, db, time.Now().UTC())
		Expect(err).NotTo(HaveOccurred())
		Expect(result.cancelled).To(BeNumerically(">=", 1))
		Expect(run.state(ctx, orphan.ID)).To(Equal(database.TurnRequestStateCancelled),
			"an expiry is a lapsed question; a run that ended cancels the question outright")
	})

	It("leaves a live pending approval alone", func(ctx SpecContext) {
		run := newSweepRun(ctx, db)
		live := run.approval(ctx, "toolu_sweep_live", "Edit", time.Now().Add(time.Hour))
		run.setRunState(ctx, database.PromptRunStateWaiting)

		_, err := sweepToolApprovals(ctx, db, time.Now().UTC())
		Expect(err).NotTo(HaveOccurred())
		Expect(run.state(ctx, live.ID)).To(Equal(database.TurnRequestStatePending),
			"a sweeper that takes away a question a person is still looking at is worse than no sweeper")
	})

	It("sweeps on its own ticker once the monitor holds the writer lock", func(ctx SpecContext) {
		GinkgoT().Setenv("HOME", GinkgoT().TempDir())
		run := newSweepRun(ctx, db)
		lapsed := run.approval(ctx, "toolu_sweep_ticker", "Write", time.Now().Add(50*time.Millisecond))

		monitor, err := New(Config{
			DB: db, HostID: "sweep-spec",
			ApprovalSweepInterval: 50 * time.Millisecond,
			DiscoverProcesses:     func() ([]Process, error) { return nil, nil },
		})
		Expect(err).NotTo(HaveOccurred())

		runCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
		done := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			done <- monitor.Run(runCtx)
		}()
		DeferCleanup(func() {
			stop()
			Eventually(done, 10*time.Second).Should(Receive(BeNil()))
		})

		Eventually(func() database.TurnRequestState { return run.state(ctx, lapsed.ID) }, 10*time.Second).
			Should(Equal(database.TurnRequestStateExpired),
				"expires_at is only enforced if some loop actually reads it")
	})

	It("defaults the sweep cadence rather than leaving the ticker unarmed", func() {
		monitor, err := New(Config{DB: db})
		Expect(err).NotTo(HaveOccurred())
		Expect(monitor.cfg.ApprovalSweepInterval).To(BeNumerically(">", time.Duration(0)))
	})
})

type sweepRun struct {
	db      *database.DB
	session uuid.UUID
	run     uuid.UUID
}

func newSweepRun(ctx context.Context, db *database.DB) *sweepRun {
	GinkgoHelper()
	session, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{
		ID: uuid.New(), Source: "captain", Provider: "anthropic",
	})
	Expect(err).NotTo(HaveOccurred())
	run, err := db.CreatePromptRun(ctx, database.CreatePromptRunInput{SessionID: session.ID})
	Expect(err).NotTo(HaveOccurred())
	return &sweepRun{db: db, session: session.ID, run: run.ID}
}

func (r *sweepRun) approval(ctx context.Context, toolCallID, tool string, expiresAt time.Time) *database.TurnRequest {
	GinkgoHelper()
	request, err := r.db.CreateToolApprovalRequest(ctx, database.CreateToolApprovalRequestInput{
		SessionID: r.session, PromptRunID: r.run, ToolCallID: toolCallID, Tool: tool,
		Input: map[string]any{"path": "main.go"}, RequestedBy: "provider", ExpiresAt: expiresAt,
	})
	Expect(err).NotTo(HaveOccurred())
	return request
}

func (r *sweepRun) setRunState(ctx context.Context, state database.PromptRunState) {
	GinkgoHelper()
	current, err := r.db.GetPromptRun(ctx, r.run)
	Expect(err).NotTo(HaveOccurred())
	_, err = r.db.UpdatePromptRun(ctx, database.UpdatePromptRunInput{
		ID: r.run, ExpectedVersion: current.Version, State: &state,
	})
	Expect(err).NotTo(HaveOccurred())
}

func (r *sweepRun) state(ctx context.Context, id uuid.UUID) database.TurnRequestState {
	GinkgoHelper()
	request, err := r.db.GetTurnRequest(ctx, id)
	Expect(err).NotTo(HaveOccurred())
	return request.State
}
