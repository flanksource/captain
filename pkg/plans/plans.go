// Package plans is Captain's plan domain service. Hosts save, approve and
// review plans through it instead of writing captain_plans or
// captain_plan_revisions themselves: every operation is one transaction that
// takes the plan row lock, applies Captain's rules, and then runs the host's
// Link hook in the same transaction so host bookkeeping commits or rolls back
// with Captain's writes.
//
// Lock order is the plan row first, then whatever rows the host Link locks.
package plans

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// Link is the host hook run inside the plan transaction after Captain's
// writes. tx is bound to that transaction; a returned error rolls back the
// Captain writes as well.
type Link func(ctx context.Context, tx *database.DB, outcome Outcome) error

// Outcome is what one plan operation did, read inside its transaction.
type Outcome struct {
	// Plan is the plan after the operation.
	Plan *database.Plan
	// Prior is the plan as it was when the row lock was acquired, before this
	// operation's revision or review change. Save creates a missing plan before
	// taking the lock, so a new plan's Prior has no revisions.
	Prior *database.Plan
	// Revision is the saved revision (Save), the approved revision (Approve),
	// or nil (Review).
	Revision *database.PlanRevision
	// Changed is false when the operation was an exact replay that wrote nothing.
	Changed bool
}

// SaveInput identifies a plan and the content of its next revision.
type SaveInput struct {
	Plan      database.CreatePlanInput
	Markdown  string
	Feedback  string
	CreatedBy string
}

// ApproveInput selects the revision to approve: exactly one of RevisionID or
// Latest. Latest resolves the newest revision under the plan lock, so a
// revision appended concurrently can never be approved unseen.
type ApproveInput struct {
	PlanID     uuid.UUID
	RevisionID uuid.UUID
	Latest     bool
	ApprovedBy string
	Comment    string
}

// ReviewInput records a non-approval decision: pending, rejected or
// revision_requested.
type ReviewInput = database.SetPlanReviewStateInput

// Save creates or resolves the plan and appends its revision. New content
// returns a decided plan to pending; equivalent content is an idempotent replay.
func Save(ctx context.Context, db *database.DB, input SaveInput, link Link) (Outcome, error) {
	var outcome Outcome
	err := db.Transaction(ctx, func(tx *database.DB) error {
		plan, err := tx.CreateOrGetPlan(ctx, input.Plan)
		if err != nil {
			return err
		}
		prior, err := tx.LockPlan(ctx, plan.ID)
		if err != nil {
			return err
		}
		revision, created, err := tx.AppendPlanRevisionWithResult(ctx, database.AppendPlanRevisionInput{
			PlanID: prior.ID, PlanMarkdown: input.Markdown, Feedback: input.Feedback, CreatedBy: input.CreatedBy,
		})
		if err != nil {
			return err
		}
		after, err := tx.GetPlan(ctx, prior.ID)
		if err != nil {
			return err
		}
		outcome = Outcome{Plan: after, Prior: prior, Revision: revision, Changed: created}
		return runLink(ctx, tx, link, outcome)
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("save Captain plan: %w", err)
	}
	return outcome, nil
}

// Approve selects one immutable revision as the plan's approved content.
func Approve(ctx context.Context, db *database.DB, input ApproveInput, link Link) (Outcome, error) {
	if input.Latest == (input.RevisionID != uuid.Nil) {
		return Outcome{}, fmt.Errorf("%w: approve plan %s needs exactly one of a revision ID or latest",
			database.ErrInvalidPlan, input.PlanID)
	}
	var outcome Outcome
	err := db.Transaction(ctx, func(tx *database.DB) error {
		prior, err := tx.LockPlan(ctx, input.PlanID)
		if err != nil {
			return err
		}
		revisionID := input.RevisionID
		if input.Latest {
			if prior.LatestRevision == nil {
				return fmt.Errorf("%w: plan %s has no revisions to approve", database.ErrPlanRevisionNotFound, prior.ID)
			}
			revisionID = prior.LatestRevision.ID
		}
		after, err := tx.ApprovePlanRevision(ctx, database.ApprovePlanRevisionInput{
			PlanID: prior.ID, RevisionID: revisionID, ApprovedBy: input.ApprovedBy, Comment: input.Comment,
		})
		if err != nil {
			return err
		}
		outcome = Outcome{Plan: after, Prior: prior, Revision: after.ApprovedRevision, Changed: changed(prior, after)}
		return runLink(ctx, tx, link, outcome)
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("approve Captain plan: %w", err)
	}
	return outcome, nil
}

// Review records a non-approval review decision on the plan.
func Review(ctx context.Context, db *database.DB, input ReviewInput, link Link) (Outcome, error) {
	var outcome Outcome
	err := db.Transaction(ctx, func(tx *database.DB) error {
		prior, err := tx.LockPlan(ctx, input.PlanID)
		if err != nil {
			return err
		}
		after, err := tx.SetPlanReviewState(ctx, input)
		if err != nil {
			return err
		}
		outcome = Outcome{Plan: after, Prior: prior, Changed: changed(prior, after)}
		return runLink(ctx, tx, link, outcome)
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("review Captain plan: %w", err)
	}
	return outcome, nil
}

// changed relies on captain_plans.updated_at, which a trigger stamps with
// clock_timestamp() on every UPDATE; the store skips the UPDATE on an exact
// replay, so an unchanged timestamp means nothing was written.
func changed(prior, after *database.Plan) bool {
	return !prior.UpdatedAt.Equal(after.UpdatedAt)
}

func runLink(ctx context.Context, tx *database.DB, link Link, outcome Outcome) error {
	if link == nil {
		return nil
	}
	if err := link(ctx, tx, outcome); err != nil {
		return fmt.Errorf("link plan %s: %w", outcome.Plan.ID, err)
	}
	return nil
}
