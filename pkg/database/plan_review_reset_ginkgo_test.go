package database

import (
	"github.com/flanksource/commons-db/dbtest"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("plan revision review reset", func() {
	const (
		firstMarkdown  = "# Plan\n\n- first approach"
		secondMarkdown = "# Plan\n\n- revised approach"
		reviewer       = "reviewer"
	)

	var (
		db   *DB
		plan *Plan
	)

	BeforeEach(func(ctx SpecContext) {
		handle := dbtest.ForGinkgo(dbtest.Options{Name: "captain_plan_review_reset"})
		var err error
		db, err = Open(ctx, WithDSN(handle.DSN()), WithMigrations())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(db.Close)
		session, err := db.CreateOrGetSession(ctx, CreateSessionInput{Source: "test", Provider: "test"})
		Expect(err).NotTo(HaveOccurred())
		plan, err = db.CreateOrGetPlan(ctx, CreatePlanInput{SourceSessionID: session.ID, Variant: "primary"})
		Expect(err).NotTo(HaveOccurred())
	})

	DescribeTable("a new revision returns a decided plan to pending",
		func(ctx SpecContext, decide func(revision *PlanRevision) error) {
			first, err := db.AppendPlanRevision(ctx, AppendPlanRevisionInput{PlanID: plan.ID, PlanMarkdown: firstMarkdown})
			Expect(err).NotTo(HaveOccurred())
			Expect(decide(first)).To(Succeed())

			second, created, err := db.AppendPlanRevisionWithResult(ctx, AppendPlanRevisionInput{
				PlanID: plan.ID, PlanMarkdown: secondMarkdown, CreatedBy: "agent",
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(created).To(BeTrue())

			reset, err := db.GetPlan(ctx, plan.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(reset.ApprovalState).To(Equal(PlanApprovalPending))
			Expect(reset.ApprovedRevisionID).To(BeNil())
			Expect(reset.ApprovedBy).To(BeEmpty())
			Expect(reset.ApprovalComment).To(BeEmpty())
			Expect(reset.ApprovalCreatedAt).To(BeNil())
			Expect(reset.FeedbackAt).To(BeNil())
			Expect(reset.LatestRevision.ID).To(Equal(second.ID))
		},
		Entry("approved", func(revision *PlanRevision) error {
			_, err := db.ApprovePlanRevision(GinkgoT().Context(), ApprovePlanRevisionInput{
				PlanID: plan.ID, RevisionID: revision.ID, ApprovedBy: reviewer, Comment: "ship it",
			})
			return err
		}),
		Entry("rejected", func(*PlanRevision) error {
			_, err := db.SetPlanReviewState(GinkgoT().Context(), SetPlanReviewStateInput{
				PlanID: plan.ID, State: PlanApprovalRejected, Actor: reviewer, Comment: "wrong approach",
			})
			return err
		}),
		Entry("revision requested", func(*PlanRevision) error {
			_, err := db.SetPlanReviewState(GinkgoT().Context(), SetPlanReviewStateInput{
				PlanID: plan.ID, State: PlanApprovalRevisionRequested, Actor: reviewer, Comment: "add rollback",
			})
			return err
		}),
	)

	It("keeps the approval when an equivalent revision is replayed", func(ctx SpecContext) {
		first, err := db.AppendPlanRevision(ctx, AppendPlanRevisionInput{PlanID: plan.ID, PlanMarkdown: firstMarkdown})
		Expect(err).NotTo(HaveOccurred())
		approved, err := db.ApprovePlanRevision(ctx, ApprovePlanRevisionInput{
			PlanID: plan.ID, RevisionID: first.ID, ApprovedBy: reviewer,
		})
		Expect(err).NotTo(HaveOccurred())

		replayed, created, err := db.AppendPlanRevisionWithResult(ctx, AppendPlanRevisionInput{
			PlanID: plan.ID, PlanMarkdown: "\r\n" + firstMarkdown + "\r\n",
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(created).To(BeFalse())
		Expect(replayed.ID).To(Equal(first.ID))

		after, err := db.GetPlan(ctx, plan.ID)
		Expect(err).NotTo(HaveOccurred())
		Expect(after.ApprovalState).To(Equal(PlanApprovalApproved))
		Expect(after.ApprovedRevisionID).To(HaveValue(Equal(first.ID)))
		Expect(after.UpdatedAt).To(Equal(approved.UpdatedAt))
	})

	It("refuses to lock a plan outside a transaction", func(ctx SpecContext) {
		_, err := db.LockPlan(ctx, plan.ID)
		Expect(err).To(MatchError(ContainSubstring("inside a Captain transaction")))
	})

	It("locks and returns the plan inside a transaction", func(ctx SpecContext) {
		Expect(db.Transaction(ctx, func(tx *DB) error {
			locked, err := tx.LockPlan(ctx, plan.ID)
			Expect(err).NotTo(HaveOccurred())
			Expect(locked.ID).To(Equal(plan.ID))
			return nil
		})).To(Succeed())
	})
})
