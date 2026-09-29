package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// LockPlan takes the plan's row lock for the rest of the caller's transaction
// and returns the plan as it is under that lock. It must run on a handle from
// DB.Transaction: a lock taken outside a transaction is released immediately
// and would silently serialize nothing.
func (db *DB) LockPlan(ctx context.Context, id uuid.UUID) (*Plan, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if !db.inTransaction() {
		return nil, fmt.Errorf("lock Captain plan %s: must run inside a Captain transaction", id)
	}
	if id == uuid.Nil {
		return nil, fmt.Errorf("%w: plan ID is required", ErrInvalidPlan)
	}
	record, err := lockPlanRecord(db.gorm.WithContext(ctx), id)
	if err != nil {
		if errors.Is(err, ErrPlanNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrPlanNotFound, id)
		}
		return nil, fmt.Errorf("lock Captain plan: %w", err)
	}
	plans, err := db.hydratePlans(ctx, []planRecord{record})
	if err != nil {
		return nil, err
	}
	return &plans[0], nil
}

func (db *DB) inTransaction() bool {
	_, ok := db.gorm.Statement.ConnPool.(gorm.TxCommitter)
	return ok
}

// lockPlanRecord serializes plan mutations. NO KEY UPDATE conflicts with every
// other plan mutator but not with the KEY SHARE locks foreign keys take, so a
// host transaction that already references the plan does not deadlock it.
func lockPlanRecord(tx *gorm.DB, id uuid.UUID) (planRecord, error) {
	var plan planRecord
	err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&plan, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return plan, ErrPlanNotFound
	}
	return plan, err
}

// resetPlanReview returns a decided plan to pending. A new immutable revision is
// never implicitly covered by a decision made on older content.
func resetPlanReview(tx *gorm.DB, plan planRecord) error {
	if plan.ApprovalState == PlanApprovalPending {
		return nil
	}
	return tx.Model(&planRecord{}).Where("id = ?", plan.ID).Updates(map[string]any{
		"approval_state":       PlanApprovalPending,
		"approved_revision_id": nil,
		"approved_by":          nil,
		"approval_comment":     nil,
		"approval_created_at":  nil,
		"feedback_at":          nil,
	}).Error
}

// ApprovePlanRevision atomically verifies the revision belongs to the plan and
// selects it as the durable approved content.
func (db *DB) ApprovePlanRevision(ctx context.Context, input ApprovePlanRevisionInput) (*Plan, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if input.PlanID == uuid.Nil || input.RevisionID == uuid.Nil {
		return nil, fmt.Errorf("%w: plan and revision IDs are required", ErrInvalidPlan)
	}
	err := db.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		plan, err := lockPlanRecord(tx, input.PlanID)
		if err != nil {
			return err
		}
		if err := requirePlanRevision(tx, input.PlanID, input.RevisionID); err != nil {
			return err
		}
		approvedBy := nullableTrimmed(input.ApprovedBy)
		comment := nullableTrimmed(input.Comment)
		if plan.ApprovalState == PlanApprovalApproved && plan.ApprovedRevisionID != nil &&
			*plan.ApprovedRevisionID == input.RevisionID && equalOptionalString(plan.ApprovedBy, approvedBy) &&
			equalOptionalString(plan.ApprovalComment, comment) {
			return nil
		}
		return tx.Model(&planRecord{}).Where("id = ?", input.PlanID).Updates(map[string]any{
			"approval_state":       PlanApprovalApproved,
			"approved_revision_id": input.RevisionID,
			"approved_by":          approvedBy,
			"approval_comment":     comment,
			"approval_created_at":  gorm.Expr("clock_timestamp()"),
		}).Error
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrPlanNotFound):
			return nil, fmt.Errorf("%w: %s", ErrPlanNotFound, input.PlanID)
		case errors.Is(err, ErrPlanRevisionNotFound):
			return nil, fmt.Errorf("%w: %s", ErrPlanRevisionNotFound, input.RevisionID)
		default:
			return nil, fmt.Errorf("approve Captain plan revision: %w", err)
		}
	}
	return db.GetPlan(ctx, input.PlanID)
}

func requirePlanRevision(tx *gorm.DB, planID, revisionID uuid.UUID) error {
	var revision planRevisionRecord
	err := tx.First(&revision, "id = ? AND plan_id = ?", revisionID, planID).Error
	if err == nil || !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	var count int64
	if err := tx.Model(&planRevisionRecord{}).Where("id = ?", revisionID).Count(&count).Error; err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("%w: revision belongs to another plan", ErrPlanConflict)
	}
	return ErrPlanRevisionNotFound
}

// SetPlanReviewState records rejection, revision feedback, or a reset to
// pending without selecting mutable/path-backed content. Exact retries are
// mutation-free.
func (db *DB) SetPlanReviewState(ctx context.Context, input SetPlanReviewStateInput) (*Plan, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if input.PlanID == uuid.Nil {
		return nil, fmt.Errorf("%w: plan ID is required", ErrInvalidPlan)
	}
	switch input.State {
	case PlanApprovalPending, PlanApprovalRejected, PlanApprovalRevisionRequested:
	case PlanApprovalApproved:
		return nil, fmt.Errorf("%w: approval must select a revision through ApprovePlanRevision", ErrInvalidPlan)
	default:
		return nil, fmt.Errorf("%w: unknown approval state %q", ErrInvalidPlan, input.State)
	}

	actor := nullableTrimmed(input.Actor)
	comment := nullableTrimmed(input.Comment)
	err := db.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		plan, err := lockPlanRecord(tx, input.PlanID)
		if err != nil {
			return err
		}
		if plan.ApprovalState == input.State && plan.ApprovedRevisionID == nil &&
			equalOptionalString(plan.ApprovedBy, actor) && equalOptionalString(plan.ApprovalComment, comment) {
			return nil
		}
		updates := map[string]any{
			"approval_state":       input.State,
			"approved_revision_id": nil,
			"approved_by":          actor,
			"approval_comment":     comment,
			"approval_created_at":  nil,
			"feedback_at":          nil,
		}
		switch input.State {
		case PlanApprovalRejected:
			// The plan-state trigger only fills this timestamp when the state
			// changes. A new rejection decision while already rejected must not
			// clear it merely because only the actor or comment changed.
			updates["approval_created_at"] = gorm.Expr("clock_timestamp()")
		case PlanApprovalRevisionRequested:
			updates["feedback_at"] = gorm.Expr("clock_timestamp()")
		}
		return tx.Model(&planRecord{}).Where("id = ?", input.PlanID).Updates(updates).Error
	})
	if err != nil {
		if errors.Is(err, ErrPlanNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrPlanNotFound, input.PlanID)
		}
		return nil, fmt.Errorf("set Captain plan review state: %w", err)
	}
	return db.GetPlan(ctx, input.PlanID)
}
