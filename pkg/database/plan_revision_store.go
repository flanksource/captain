package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// AppendPlanRevision appends a monotonically numbered revision while holding a
// row lock on the plan. Equivalent LF/CRLF and surrounding whitespace content
// is idempotent because its normalized SHA-256 hash is unique per plan.
func (db *DB) AppendPlanRevision(ctx context.Context, input AppendPlanRevisionInput) (*PlanRevision, error) {
	revision, _, err := db.AppendPlanRevisionWithResult(ctx, input)
	return revision, err
}

// AppendPlanRevisionWithResult appends or resolves an idempotent plan revision.
// Created is true only when this call inserted the revision while holding the
// plan row lock; an equivalent existing content hash returns false. A created
// revision returns an approved, rejected or revision-requested plan to pending
// in the same transaction, because no earlier decision covers new content.
func (db *DB) AppendPlanRevisionWithResult(ctx context.Context, input AppendPlanRevisionInput) (*PlanRevision, bool, error) {
	if err := db.requireGorm(); err != nil {
		return nil, false, err
	}
	if input.PlanID == uuid.Nil {
		return nil, false, fmt.Errorf("%w: plan ID is required", ErrInvalidPlan)
	}
	markdown := normalizePlanMarkdown(input.PlanMarkdown)
	if markdown == "" {
		return nil, false, fmt.Errorf("%w: plan markdown is empty", ErrInvalidPlan)
	}
	hash := planContentHash(markdown)
	var revision planRevisionRecord
	created := false
	err := db.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		plan, err := lockPlanRecord(tx, input.PlanID)
		if err != nil {
			return err
		}
		if err := tx.Where("plan_id = ? AND content_hash = ?", input.PlanID, hash).First(&revision).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}

		var latest int
		if err := tx.Model(&planRevisionRecord{}).
			Where("plan_id = ?", input.PlanID).
			Select("COALESCE(MAX(revision), 0)").Scan(&latest).Error; err != nil {
			return err
		}
		revision = planRevisionRecord{
			ID:           uuid.New(),
			PlanID:       input.PlanID,
			Revision:     latest + 1,
			PlanMarkdown: markdown,
			ContentHash:  hash,
			Feedback:     nullableTrimmed(input.Feedback),
			CreatedBy:    nullableTrimmed(input.CreatedBy),
		}
		if err := tx.Create(&revision).Error; err != nil {
			return err
		}
		created = true
		return resetPlanReview(tx, plan)
	})
	if err != nil {
		if errors.Is(err, ErrPlanNotFound) {
			return nil, false, fmt.Errorf("%w: %s", ErrPlanNotFound, input.PlanID)
		}
		return nil, false, fmt.Errorf("append Captain plan revision: %w", err)
	}
	out := revisionFromRecord(revision)
	return &out, created, nil
}

func (db *DB) GetPlanRevision(ctx context.Context, planID, revisionID uuid.UUID) (*PlanRevision, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var record planRevisionRecord
	if err := db.gorm.WithContext(ctx).First(&record, "plan_id = ? AND id = ?", planID, revisionID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrPlanRevisionNotFound, revisionID)
		}
		return nil, fmt.Errorf("get Captain plan revision: %w", err)
	}
	out := revisionFromRecord(record)
	return &out, nil
}

func (db *DB) GetApprovedPlanRevision(ctx context.Context, planID uuid.UUID) (*PlanRevision, error) {
	plan, err := db.GetPlan(ctx, planID)
	if err != nil {
		return nil, err
	}
	if plan.ApprovedRevision == nil {
		return nil, fmt.Errorf("%w: plan %s has no approved revision", ErrPlanRevisionNotFound, planID)
	}
	return plan.ApprovedRevision, nil
}

func (db *DB) ListPlanRevisions(ctx context.Context, planID uuid.UUID) ([]PlanRevision, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var exists int64
	if err := db.gorm.WithContext(ctx).Model(&planRecord{}).Where("id = ?", planID).Count(&exists).Error; err != nil {
		return nil, fmt.Errorf("check Captain plan: %w", err)
	}
	if exists == 0 {
		return nil, fmt.Errorf("%w: %s", ErrPlanNotFound, planID)
	}
	var records []planRevisionRecord
	if err := db.gorm.WithContext(ctx).Where("plan_id = ?", planID).Order("revision ASC").Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list Captain plan revisions: %w", err)
	}
	out := make([]PlanRevision, len(records))
	for i := range records {
		out[i] = revisionFromRecord(records[i])
	}
	return out, nil
}
