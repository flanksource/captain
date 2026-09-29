package plans_test

import (
	"context"
	"errors"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/plans"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("plans.Approve and plans.Review", func() {
	var (
		db     *database.DB
		planID uuid.UUID
		first  plans.Outcome
		latest plans.Outcome
	)

	BeforeEach(func(ctx SpecContext) {
		var sessionID uuid.UUID
		db, sessionID = openPlanDB(ctx, "captain_plans_review")
		planID = uuid.New()
		input := saveInput(sessionID, firstMarkdown)
		input.Plan.ID = planID
		var err error
		first, err = plans.Save(ctx, db, input, nil)
		Expect(err).NotTo(HaveOccurred())
		input.Markdown = secondMarkdown
		latest, err = plans.Save(ctx, db, input, nil)
		Expect(err).NotTo(HaveOccurred())
	})

	It("approves the latest revision resolved under the plan lock", func(ctx SpecContext) {
		var seen []plans.Outcome
		approved, err := plans.Approve(ctx, db, plans.ApproveInput{
			PlanID: planID, Latest: true, ApprovedBy: reviewer, Comment: "ship it",
		}, recordLink(&seen))

		Expect(err).NotTo(HaveOccurred())
		Expect(approved.Changed).To(BeTrue())
		Expect(approved.Prior.ApprovalState).To(Equal(database.PlanApprovalPending))
		Expect(approved.Plan.ApprovalState).To(Equal(database.PlanApprovalApproved))
		Expect(approved.Plan.ApprovedBy).To(Equal(reviewer))
		Expect(approved.Plan.ApprovalComment).To(Equal("ship it"))
		Expect(approved.Revision.ID).To(Equal(latest.Revision.ID))
		Expect(seen).To(Equal([]plans.Outcome{approved}))

		replayed, err := plans.Approve(ctx, db, plans.ApproveInput{
			PlanID: planID, RevisionID: latest.Revision.ID, ApprovedBy: reviewer, Comment: "ship it",
		}, nil)
		Expect(err).NotTo(HaveOccurred())
		Expect(replayed.Changed).To(BeFalse())
	})

	It("approves an exact older revision", func(ctx SpecContext) {
		approved, err := plans.Approve(ctx, db, plans.ApproveInput{
			PlanID: planID, RevisionID: first.Revision.ID, ApprovedBy: reviewer,
		}, nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(approved.Revision.ID).To(Equal(first.Revision.ID))
		Expect(approved.Plan.ApprovedRevisionID).To(HaveValue(Equal(first.Revision.ID)))
	})

	DescribeTable("refuses an ambiguous revision selector",
		func(ctx SpecContext, revisionID func() uuid.UUID, latest bool) {
			_, err := plans.Approve(ctx, db, plans.ApproveInput{
				PlanID: planID, RevisionID: revisionID(), Latest: latest, ApprovedBy: reviewer,
			}, nil)

			Expect(err).To(MatchError(database.ErrInvalidPlan))
		},
		Entry("neither a revision nor latest", func() uuid.UUID { return uuid.Nil }, false),
		Entry("both a revision and latest", func() uuid.UUID { return first.Revision.ID }, true),
	)

	It("refuses to approve the latest revision of a plan that has none", func(ctx SpecContext) {
		empty, err := db.CreateOrGetPlan(ctx, database.CreatePlanInput{
			ID: uuid.New(), SourceSessionID: first.Plan.SourceSessionID, Variant: "empty",
		})
		Expect(err).NotTo(HaveOccurred())

		_, err = plans.Approve(ctx, db, plans.ApproveInput{PlanID: empty.ID, Latest: true, ApprovedBy: reviewer}, nil)

		Expect(err).To(MatchError(database.ErrPlanRevisionNotFound))
	})

	DescribeTable("records a non-approval review decision",
		func(ctx SpecContext, state database.PlanApprovalState, comment string) {
			reviewed, err := plans.Review(ctx, db, plans.ReviewInput{
				PlanID: planID, State: state, Actor: reviewer, Comment: comment,
			}, nil)

			Expect(err).NotTo(HaveOccurred())
			Expect(reviewed.Changed).To(BeTrue())
			Expect(reviewed.Prior.ApprovalState).To(Equal(database.PlanApprovalPending))
			Expect(reviewed.Plan.ApprovalState).To(Equal(state))
			Expect(reviewed.Plan.ApprovalComment).To(Equal(comment))
			Expect(reviewed.Revision).To(BeNil())

			replayed, err := plans.Review(ctx, db, plans.ReviewInput{
				PlanID: planID, State: state, Actor: reviewer, Comment: comment,
			}, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(replayed.Changed).To(BeFalse())
		},
		Entry("rejected", database.PlanApprovalRejected, "wrong approach"),
		Entry("revision requested", database.PlanApprovalRevisionRequested, "add rollback coverage"),
	)

	DescribeTable("rolls every captain write back when the host link fails",
		func(ctx SpecContext, mutate func(context.Context, plans.Link) error) {
			before, err := db.GetPlan(ctx, planID)
			Expect(err).NotTo(HaveOccurred())
			linkErr := errors.New("host selection lost its version race")

			err = mutate(ctx, func(context.Context, *database.DB, plans.Outcome) error { return linkErr })

			Expect(err).To(MatchError(linkErr))
			after, err := db.GetPlan(ctx, planID)
			Expect(err).NotTo(HaveOccurred())
			Expect(after).To(Equal(before))
		},
		Entry("save", func(ctx context.Context, link plans.Link) error {
			input := saveInput(first.Plan.SourceSessionID, "# Plan\n\n- third approach")
			input.Plan.ID = planID
			_, err := plans.Save(ctx, db, input, link)
			return err
		}),
		Entry("approve", func(ctx context.Context, link plans.Link) error {
			_, err := plans.Approve(ctx, db, plans.ApproveInput{PlanID: planID, Latest: true, ApprovedBy: reviewer}, link)
			return err
		}),
		Entry("review", func(ctx context.Context, link plans.Link) error {
			_, err := plans.Review(ctx, db, plans.ReviewInput{
				PlanID: planID, State: database.PlanApprovalRejected, Actor: reviewer,
			}, link)
			return err
		}),
	)

	It("holds the plan lock until the host link finishes", func(ctx SpecContext) {
		entered, release := make(chan struct{}), make(chan struct{})
		approveDone := make(chan error, 1)
		go func() {
			defer GinkgoRecover()
			_, err := plans.Approve(ctx, db, plans.ApproveInput{PlanID: planID, Latest: true, ApprovedBy: reviewer},
				func(context.Context, *database.DB, plans.Outcome) error {
					close(entered)
					<-release
					return nil
				})
			approveDone <- err
		}()
		Eventually(entered).Should(BeClosed())

		reviewDone := make(chan plans.Outcome, 1)
		go func() {
			defer GinkgoRecover()
			reviewed, err := plans.Review(ctx, db, plans.ReviewInput{
				PlanID: planID, State: database.PlanApprovalRejected, Actor: reviewer,
			}, nil)
			Expect(err).NotTo(HaveOccurred())
			reviewDone <- reviewed
		}()
		Consistently(reviewDone, 300*time.Millisecond).ShouldNot(Receive())

		close(release)
		Eventually(approveDone).Should(Receive(BeNil()))
		var reviewed plans.Outcome
		Eventually(reviewDone).Should(Receive(&reviewed))
		Expect(reviewed.Prior.ApprovalState).To(Equal(database.PlanApprovalApproved))
		Expect(reviewed.Plan.ApprovalState).To(Equal(database.PlanApprovalRejected))
	})
})
