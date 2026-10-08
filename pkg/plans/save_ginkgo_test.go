package plans_test

import (
	"context"
	"errors"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/plans"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

const (
	firstMarkdown  = "# Plan\n\n- first approach"
	secondMarkdown = "# Plan\n\n- revised approach"
	reviewer       = "reviewer"
	planAuthor     = "agent"
	planVariant    = "primary"
	planProfile    = "host.plan"
)

// openPlanDB leases a migrated database and records the source session every
// plan in these specs hangs off.
func openPlanDB(ctx context.Context, name string) (*database.DB, uuid.UUID) {
	handle := dbtest.ForGinkgo(dbtest.Options{Name: name})
	db, err := database.Open(ctx, database.WithDSN(handle.DSN()), database.WithMigrations())
	Expect(err).NotTo(HaveOccurred())
	DeferCleanup(db.Close)
	session, err := db.CreateOrGetSession(ctx, database.CreateSessionInput{Source: "test", Provider: "test"})
	Expect(err).NotTo(HaveOccurred())
	return db, session.ID
}

func saveInput(sessionID uuid.UUID, markdown string) plans.SaveInput {
	return plans.SaveInput{
		Plan: database.CreatePlanInput{
			SourceSessionID: sessionID, Title: "Durable plan", Variant: planVariant, SpecProfile: planProfile,
		},
		Markdown:  markdown,
		CreatedBy: planAuthor,
	}
}

// recordLink captures every outcome the service hands the host.
func recordLink(seen *[]plans.Outcome) plans.Link {
	return func(_ context.Context, _ *database.DB, outcome plans.Outcome) error {
		*seen = append(*seen, outcome)
		return nil
	}
}

var _ = Describe("plans.Save", func() {
	var (
		db        *database.DB
		sessionID uuid.UUID
		planID    uuid.UUID
	)

	BeforeEach(func(ctx SpecContext) {
		db, sessionID = openPlanDB(ctx, "captain_plans_save")
		planID = uuid.New()
	})

	withID := func(markdown string) plans.SaveInput {
		input := saveInput(sessionID, markdown)
		input.Plan.ID = planID
		return input
	}

	It("creates the plan and its first revision and hands both to the host link", func(ctx SpecContext) {
		var seen []plans.Outcome
		outcome, err := plans.Save(ctx, db, withID(firstMarkdown), recordLink(&seen))

		Expect(err).NotTo(HaveOccurred())
		Expect(outcome.Changed).To(BeTrue())
		Expect(outcome.Plan.ID).To(Equal(planID))
		Expect(outcome.Plan.SpecProfile).To(Equal(planProfile))
		Expect(outcome.Plan.ApprovalState).To(Equal(database.PlanApprovalPending))
		Expect(outcome.Revision.Revision).To(Equal(1))
		Expect(outcome.Revision.CreatedBy).To(Equal(planAuthor))
		Expect(outcome.Plan.LatestRevision.ID).To(Equal(outcome.Revision.ID))
		Expect(outcome.Prior.LatestRevision).To(BeNil(), "the prior state is the plan before this revision")
		Expect(seen).To(Equal([]plans.Outcome{outcome}))
	})

	It("is idempotent for equivalent content", func(ctx SpecContext) {
		first, err := plans.Save(ctx, db, withID(firstMarkdown), nil)
		Expect(err).NotTo(HaveOccurred())

		replayed, err := plans.Save(ctx, db, withID("\r\n"+firstMarkdown+"\r\n"), nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(replayed.Changed).To(BeFalse())
		Expect(replayed.Plan.ID).To(Equal(first.Plan.ID))
		Expect(replayed.Revision.ID).To(Equal(first.Revision.ID))
		revisions, err := db.ListPlanRevisions(ctx, planID)
		Expect(err).NotTo(HaveOccurred())
		Expect(revisions).To(HaveLen(1))
	})

	It("returns an approved plan to pending when a new revision arrives", func(ctx SpecContext) {
		first, err := plans.Save(ctx, db, withID(firstMarkdown), nil)
		Expect(err).NotTo(HaveOccurred())
		_, err = plans.Approve(ctx, db, plans.ApproveInput{PlanID: planID, Latest: true, ApprovedBy: reviewer}, nil)
		Expect(err).NotTo(HaveOccurred())

		revised, err := plans.Save(ctx, db, withID(secondMarkdown), nil)

		Expect(err).NotTo(HaveOccurred())
		Expect(revised.Changed).To(BeTrue())
		Expect(revised.Prior.ApprovalState).To(Equal(database.PlanApprovalApproved))
		Expect(revised.Prior.ApprovedRevisionID).To(HaveValue(Equal(first.Revision.ID)))
		Expect(revised.Revision.Revision).To(Equal(2))
		Expect(revised.Plan.ApprovalState).To(Equal(database.PlanApprovalPending))
		Expect(revised.Plan.ApprovedRevisionID).To(BeNil())
		Expect(revised.Plan.ApprovedBy).To(BeEmpty())
	})

	It("runs the link inside the uncommitted plan transaction", func(ctx SpecContext) {
		var visibleOutside error
		link := func(ctx context.Context, tx *database.DB, outcome plans.Outcome) error {
			if _, err := tx.GetPlan(ctx, outcome.Plan.ID); err != nil {
				return err
			}
			_, visibleOutside = db.GetPlan(ctx, outcome.Plan.ID)
			return nil
		}

		_, err := plans.Save(ctx, db, withID(firstMarkdown), link)

		Expect(err).NotTo(HaveOccurred())
		Expect(visibleOutside).To(MatchError(database.ErrPlanNotFound))
	})

	It("composes inside a host transaction that later rolls back", func(ctx SpecContext) {
		hostErr := errors.New("host bookkeeping failed after the save")

		err := db.Transaction(ctx, func(tx *database.DB) error {
			if _, err := plans.Save(ctx, tx, withID(firstMarkdown), nil); err != nil {
				return err
			}
			return hostErr
		})

		Expect(err).To(MatchError(hostErr))
		_, err = db.GetPlan(ctx, planID)
		Expect(err).To(MatchError(database.ErrPlanNotFound))
	})

	It("refuses empty markdown before writing a plan", func(ctx SpecContext) {
		_, err := plans.Save(ctx, db, withID("  \n"), nil)

		Expect(err).To(MatchError(database.ErrInvalidPlan))
		_, err = db.GetPlan(ctx, planID)
		Expect(err).To(MatchError(database.ErrPlanNotFound))
	})
})
