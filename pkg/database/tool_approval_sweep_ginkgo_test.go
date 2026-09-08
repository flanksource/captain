package database

import (
	"context"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
)

// expires_at was data nobody enforced: the only code that read it lived inside
// the broker's own wait loop, so an approval outlived the process that raised it
// and stayed `pending` for good. ListStaleToolApprovals is the query a sweeper
// running outside that process asks — and the specs that matter most are the
// rows it must refuse to name.
var _ = Describe("ListStaleToolApprovals", Ordered, func() {
	var db *DB
	var fixture *approvalFixture

	BeforeAll(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_approval_sweep"})
		opened, err := Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(opened.Close()).To(Succeed()) })
		db = opened
	})

	BeforeEach(func(ctx SpecContext) {
		fixture = newApprovalFixture(ctx, db)
	})

	It("names an approval whose own expiry has lapsed", func(ctx SpecContext) {
		lapsed := fixture.approval(ctx, "toolu_lapsed", "Write", time.Now().Add(50*time.Millisecond))
		Eventually(func(g Gomega) []StaleToolApproval {
			stale, err := db.ListStaleToolApprovals(ctx, time.Now().UTC())
			g.Expect(err).NotTo(HaveOccurred())
			return fixture.mine(stale)
		}, 2*time.Second).Should(ConsistOf(MatchFields(IgnoreExtras, Fields{
			"ID":     Equal(lapsed.ID),
			"Tool":   Equal("Write"),
			"Lapsed": BeTrue(),
		})))
	})

	It("leaves a pending approval alone while its window is still open", func(ctx SpecContext) {
		fixture.approval(ctx, "toolu_live", "Bash", time.Now().Add(time.Hour))
		stale, err := db.ListStaleToolApprovals(ctx, time.Now().UTC())
		Expect(err).NotTo(HaveOccurred())
		Expect(fixture.mine(stale)).To(BeEmpty(),
			"a live question a person may still answer is not the sweeper's to take away")
	})

	It("names an approval whose prompt run has already finished", func(ctx SpecContext) {
		orphan := fixture.approval(ctx, "toolu_orphan", "Edit", time.Now().Add(time.Hour))
		fixture.setRunState(ctx, PromptRunStateFailed)

		stale, err := db.ListStaleToolApprovals(ctx, time.Now().UTC())
		Expect(err).NotTo(HaveOccurred())
		Expect(fixture.mine(stale)).To(ConsistOf(MatchFields(IgnoreExtras, Fields{
			"ID":       Equal(orphan.ID),
			"Lapsed":   BeFalse(),
			"RunState": Equal(PromptRunStateFailed),
		})), "nothing can answer a question asked on behalf of a run that is over")
	})

	DescribeTable("leaves an approval alone while its prompt run can still resume",
		func(ctx SpecContext, state PromptRunState) {
			fixture.approval(ctx, "toolu_"+string(state), "Bash", time.Now().Add(time.Hour))
			fixture.setRunState(ctx, state)
			stale, err := db.ListStaleToolApprovals(ctx, time.Now().UTC())
			Expect(err).NotTo(HaveOccurred())
			Expect(fixture.mine(stale)).To(BeEmpty())
		},
		Entry("waiting", PromptRunStateWaiting),
		Entry("running", PromptRunStateRunning),
		Entry("pending", PromptRunStatePending),
	)

	It("ignores an approval that already reached a decision", func(ctx SpecContext) {
		resolved := fixture.approval(ctx, "toolu_resolved", "Bash", time.Now().Add(50*time.Millisecond))
		fixture.setRunState(ctx, PromptRunStateWaiting)
		_, err := db.ResolveToolApprovalRequest(ctx, ResolveToolApprovalRequestInput{
			SessionID: fixture.session, RequestID: resolved.ID, Approved: true, ResolvedBy: "operator",
		})
		Expect(err).NotTo(HaveOccurred())

		Consistently(func(g Gomega) []StaleToolApproval {
			stale, listErr := db.ListStaleToolApprovals(ctx, time.Now().UTC())
			g.Expect(listErr).NotTo(HaveOccurred())
			return fixture.mine(stale)
		}, 300*time.Millisecond).Should(BeEmpty(),
			"re-expiring an answered approval would overwrite the answer")
	})
})

// approvalFixture is one session and prompt run whose approvals a spec can name
// without seeing every other spec's rows in the shared database.
type approvalFixture struct {
	db      *DB
	session uuid.UUID
	run     uuid.UUID
}

func newApprovalFixture(ctx context.Context, db *DB) *approvalFixture {
	GinkgoHelper()
	session, err := db.CreateOrGetSession(ctx, CreateSessionInput{
		ID: uuid.New(), Source: "captain", Provider: "anthropic",
	})
	Expect(err).NotTo(HaveOccurred())
	run, err := db.CreatePromptRun(ctx, CreatePromptRunInput{SessionID: session.ID})
	Expect(err).NotTo(HaveOccurred())
	return &approvalFixture{db: db, session: session.ID, run: run.ID}
}

func (f *approvalFixture) approval(ctx context.Context, toolCallID, tool string, expiresAt time.Time) *TurnRequest {
	GinkgoHelper()
	request, err := f.db.CreateToolApprovalRequest(ctx, CreateToolApprovalRequestInput{
		SessionID: f.session, PromptRunID: f.run, ToolCallID: toolCallID, Tool: tool,
		Input: map[string]any{"path": "main.go"}, RequestedBy: "provider", ExpiresAt: expiresAt,
	})
	Expect(err).NotTo(HaveOccurred())
	return request
}

func (f *approvalFixture) setRunState(ctx context.Context, state PromptRunState) {
	GinkgoHelper()
	current, err := f.db.GetPromptRun(ctx, f.run)
	Expect(err).NotTo(HaveOccurred())
	_, err = f.db.UpdatePromptRun(ctx, UpdatePromptRunInput{
		ID: f.run, ExpectedVersion: current.Version, State: &state,
	})
	Expect(err).NotTo(HaveOccurred())
}

func (f *approvalFixture) mine(stale []StaleToolApproval) []StaleToolApproval {
	var out []StaleToolApproval
	for _, candidate := range stale {
		if candidate.PromptRunID != nil && *candidate.PromptRunID == f.run {
			out = append(out, candidate)
		}
	}
	return out
}
