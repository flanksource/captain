package database

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidPlan          = errors.New("invalid Captain plan")
	ErrPlanNotFound         = errors.New("Captain plan not found")
	ErrPlanRevisionNotFound = errors.New("Captain plan revision not found")
	ErrPlanConflict         = errors.New("Captain plan conflict")
)

// PlanApprovalState is the durable approval state of a plan.
type PlanApprovalState string

const (
	PlanApprovalPending           PlanApprovalState = "pending"
	PlanApprovalApproved          PlanApprovalState = "approved"
	PlanApprovalRejected          PlanApprovalState = "rejected"
	PlanApprovalRevisionRequested PlanApprovalState = "revision_requested"
)

// Plan is a durable plan variant and its current immutable revisions. Paths are
// retained as source metadata only; callers should render revision content.
type Plan struct {
	ID                 uuid.UUID         `json:"id"`
	SourceSessionID    uuid.UUID         `json:"sourceSessionId"`
	SourcePromptRunID  *uuid.UUID        `json:"sourcePromptRunId,omitempty"`
	SourceIterationID  *uuid.UUID        `json:"sourceIterationId,omitempty"`
	SourceTurnID       *uuid.UUID        `json:"sourceTurnId,omitempty"`
	Title              string            `json:"title,omitempty"`
	Slug               string            `json:"slug,omitempty"`
	Path               string            `json:"path,omitempty"`
	Variant            string            `json:"variant,omitempty"`
	SpecProfile        string            `json:"specProfile,omitempty"`
	ApprovalState      PlanApprovalState `json:"approvalState"`
	ApprovedRevisionID *uuid.UUID        `json:"approvedRevisionId,omitempty"`
	ApprovalComment    string            `json:"approvalComment,omitempty"`
	ApprovedBy         string            `json:"approvedBy,omitempty"`
	ApprovalCreatedAt  *time.Time        `json:"approvalCreatedAt,omitempty"`
	FeedbackAt         *time.Time        `json:"feedbackAt,omitempty"`
	CreatedAt          time.Time         `json:"createdAt"`
	UpdatedAt          time.Time         `json:"updatedAt"`
	LatestRevision     *PlanRevision     `json:"latestRevision,omitempty"`
	ApprovedRevision   *PlanRevision     `json:"approvedRevision,omitempty"`
}

// PlanRevision is one immutable, content-addressed plan revision.
type PlanRevision struct {
	ID           uuid.UUID `json:"id"`
	PlanID       uuid.UUID `json:"planId"`
	Revision     int       `json:"revision"`
	PlanMarkdown string    `json:"planMarkdown"`
	ContentHash  string    `json:"contentHash"`
	Feedback     string    `json:"feedback,omitempty"`
	CreatedBy    string    `json:"createdBy,omitempty"`
	CreatedAt    time.Time `json:"createdAt"`
}

// CreatePlanInput identifies a new plan variant. When SourcePromptRunID and
// Variant are both supplied, CreateOrGetPlan is idempotent on that pair.
// Supplying ID also makes retries idempotent on that ID.
type CreatePlanInput struct {
	ID                uuid.UUID
	SourceSessionID   uuid.UUID
	SourcePromptRunID *uuid.UUID
	SourceIterationID *uuid.UUID
	SourceTurnID      *uuid.UUID
	Title             string
	Slug              string
	Path              string
	Variant           string
	SpecProfile       string
}

// PlanFilter limits ListPlans. Nil fields are not filtered.
type PlanFilter struct {
	SourceSessionID   *uuid.UUID
	SourcePromptRunID *uuid.UUID
	ApprovalState     *PlanApprovalState
	Variant           *string
}

type AppendPlanRevisionInput struct {
	PlanID       uuid.UUID
	PlanMarkdown string
	Feedback     string
	CreatedBy    string
}

type ApprovePlanRevisionInput struct {
	PlanID     uuid.UUID
	RevisionID uuid.UUID
	ApprovedBy string
	Comment    string
}

type planRecord struct {
	ID                 uuid.UUID         `gorm:"column:id;type:uuid;primaryKey"`
	SourceSessionID    uuid.UUID         `gorm:"column:source_session_id;type:uuid"`
	SourcePromptRunID  *uuid.UUID        `gorm:"column:source_prompt_run_id;type:uuid"`
	SourceIterationID  *uuid.UUID        `gorm:"column:source_iteration_id;type:uuid"`
	SourceTurnID       *uuid.UUID        `gorm:"column:source_turn_id;type:uuid"`
	Title              *string           `gorm:"column:title"`
	Slug               *string           `gorm:"column:slug"`
	Path               *string           `gorm:"column:path"`
	Variant            *string           `gorm:"column:variant"`
	SpecProfile        *string           `gorm:"column:spec_profile"`
	ApprovalState      PlanApprovalState `gorm:"column:approval_state"`
	ApprovedRevisionID *uuid.UUID        `gorm:"column:approved_revision_id;type:uuid"`
	ApprovalComment    *string           `gorm:"column:approval_comment"`
	ApprovedBy         *string           `gorm:"column:approved_by"`
	ApprovalCreatedAt  *time.Time        `gorm:"column:approval_created_at"`
	FeedbackAt         *time.Time        `gorm:"column:feedback_at"`
	CreatedAt          time.Time         `gorm:"column:created_at"`
	UpdatedAt          time.Time         `gorm:"column:updated_at"`
}

func (planRecord) TableName() string { return "captain_plans" }

type planRevisionRecord struct {
	ID           uuid.UUID `gorm:"column:id;type:uuid;primaryKey"`
	PlanID       uuid.UUID `gorm:"column:plan_id;type:uuid"`
	Revision     int       `gorm:"column:revision"`
	PlanMarkdown string    `gorm:"column:plan_markdown"`
	ContentHash  string    `gorm:"column:content_hash"`
	Feedback     *string   `gorm:"column:feedback"`
	CreatedBy    *string   `gorm:"column:created_by"`
	CreatedAt    time.Time `gorm:"column:created_at"`
}

func (planRevisionRecord) TableName() string { return "captain_plan_revisions" }

// CreateOrGetPlan creates a plan or returns the existing plan for the same
// prompt-run variant/caller-supplied ID. It never overwrites an existing plan.
func (db *DB) CreateOrGetPlan(ctx context.Context, input CreatePlanInput) (*Plan, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if input.SourceSessionID == uuid.Nil {
		return nil, fmt.Errorf("%w: source session ID is required", ErrInvalidPlan)
	}
	if _, err := db.GetSession(ctx, input.SourceSessionID); err != nil {
		return nil, err
	}
	if input.SourceIterationID != nil && input.SourcePromptRunID == nil {
		return nil, fmt.Errorf("%w: source iteration requires a prompt run", ErrInvalidPlan)
	}
	if input.SourcePromptRunID != nil {
		if *input.SourcePromptRunID == uuid.Nil {
			return nil, fmt.Errorf("%w: source prompt run ID cannot be nil UUID", ErrInvalidPlan)
		}
		run, err := db.GetPromptRun(ctx, *input.SourcePromptRunID)
		if err != nil {
			return nil, err
		}
		if run.SessionID != input.SourceSessionID {
			return nil, fmt.Errorf("%w: prompt run %s belongs to session %s", ErrPlanConflict, run.ID, run.SessionID)
		}
	}
	if input.SourceTurnID != nil {
		if *input.SourceTurnID == uuid.Nil {
			return nil, fmt.Errorf("%w: source turn ID cannot be nil UUID", ErrInvalidPlan)
		}
		var turn struct {
			SessionID uuid.UUID
		}
		result := db.gorm.WithContext(ctx).Raw(
			`SELECT session_id FROM captain_turns WHERE id = ?`, *input.SourceTurnID,
		).Scan(&turn)
		if result.Error != nil {
			return nil, fmt.Errorf("validate Captain plan source turn: %w", result.Error)
		}
		if result.RowsAffected == 0 {
			return nil, fmt.Errorf("%w: source turn %s does not exist", ErrPlanConflict, *input.SourceTurnID)
		}
		if turn.SessionID != input.SourceSessionID {
			return nil, fmt.Errorf("%w: source turn %s belongs to session %s", ErrPlanConflict, *input.SourceTurnID, turn.SessionID)
		}
	}
	callerSuppliedID := input.ID != uuid.Nil
	if !callerSuppliedID {
		input.ID = uuid.New()
	}
	variant := nullableTrimmed(input.Variant)
	record := planRecord{
		ID:                input.ID,
		SourceSessionID:   input.SourceSessionID,
		SourcePromptRunID: input.SourcePromptRunID,
		SourceIterationID: input.SourceIterationID,
		SourceTurnID:      input.SourceTurnID,
		Title:             nullableTrimmed(input.Title),
		Slug:              nullableTrimmed(input.Slug),
		Path:              nullableTrimmed(input.Path),
		Variant:           variant,
		SpecProfile:       nullableTrimmed(input.SpecProfile),
		ApprovalState:     PlanApprovalPending,
	}
	result := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return nil, fmt.Errorf("create Captain plan: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return db.GetPlan(ctx, record.ID)
	}

	var existing planRecord
	query := db.gorm.WithContext(ctx)
	if input.SourcePromptRunID != nil && variant != nil {
		query = query.Where("source_prompt_run_id = ? AND variant = ?", *input.SourcePromptRunID, *variant)
	} else {
		query = query.Where("id = ?", input.ID)
	}
	if err := query.First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: create was rejected by a conflicting identity", ErrPlanConflict)
		}
		return nil, fmt.Errorf("read existing Captain plan: %w", err)
	}
	if existing.SourceSessionID != input.SourceSessionID {
		return nil, fmt.Errorf("%w: existing plan belongs to session %s", ErrPlanConflict, existing.SourceSessionID)
	}
	if callerSuppliedID && existing.ID != input.ID {
		return nil, fmt.Errorf("%w: prompt variant already belongs to plan %s, not caller-supplied ID %s", ErrPlanConflict, existing.ID, input.ID)
	}
	return db.GetPlan(ctx, existing.ID)
}

// AppendPlanRevision appends a monotonically numbered revision while holding a
// row lock on the plan. Equivalent LF/CRLF and surrounding whitespace content
// is idempotent because its normalized SHA-256 hash is unique per plan.
func (db *DB) AppendPlanRevision(ctx context.Context, input AppendPlanRevisionInput) (*PlanRevision, error) {
	revision, _, err := db.AppendPlanRevisionWithResult(ctx, input)
	return revision, err
}

// AppendPlanRevisionWithResult appends or resolves an idempotent plan revision.
// Created is true only when this call inserted the revision while holding the
// plan row lock; an equivalent existing content hash returns false.
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
		var plan planRecord
		// NO KEY UPDATE serializes revision allocation without conflicting with
		// the KEY SHARE locks held by foreign keys that already reference this
		// plan in the caller's transaction.
		if err := tx.Clauses(clause.Locking{Strength: "NO KEY UPDATE"}).First(&plan, "id = ?", input.PlanID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPlanNotFound
			}
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
		return nil
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
		var plan planRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&plan, "id = ?", input.PlanID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrPlanNotFound
			}
			return err
		}
		var revision planRevisionRecord
		if err := tx.First(&revision, "id = ? AND plan_id = ?", input.RevisionID, input.PlanID).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var count int64
			if err := tx.Model(&planRevisionRecord{}).Where("id = ?", input.RevisionID).Count(&count).Error; err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("%w: revision belongs to another plan", ErrPlanConflict)
			}
			return ErrPlanRevisionNotFound
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

func (db *DB) GetPlan(ctx context.Context, id uuid.UUID) (*Plan, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, fmt.Errorf("%w: plan ID is required", ErrInvalidPlan)
	}
	var record planRecord
	if err := db.gorm.WithContext(ctx).First(&record, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrPlanNotFound, id)
		}
		return nil, fmt.Errorf("get Captain plan: %w", err)
	}
	plans, err := db.hydratePlans(ctx, []planRecord{record})
	if err != nil {
		return nil, err
	}
	return &plans[0], nil
}

func (db *DB) ListPlans(ctx context.Context, filter PlanFilter) ([]Plan, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	query := db.gorm.WithContext(ctx).Order("created_at DESC, id DESC")
	if filter.SourceSessionID != nil {
		query = query.Where("source_session_id = ?", *filter.SourceSessionID)
	}
	if filter.SourcePromptRunID != nil {
		query = query.Where("source_prompt_run_id = ?", *filter.SourcePromptRunID)
	}
	if filter.ApprovalState != nil {
		if !validPlanApprovalState(*filter.ApprovalState) {
			return nil, fmt.Errorf("%w: unknown approval state %q", ErrInvalidPlan, *filter.ApprovalState)
		}
		query = query.Where("approval_state = ?", *filter.ApprovalState)
	}
	if filter.Variant != nil {
		query = query.Where("variant = ?", strings.TrimSpace(*filter.Variant))
	}
	var records []planRecord
	if err := query.Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list Captain plans: %w", err)
	}
	return db.hydratePlans(ctx, records)
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

func (db *DB) hydratePlans(ctx context.Context, records []planRecord) ([]Plan, error) {
	plans := make([]Plan, len(records))
	if len(records) == 0 {
		return plans, nil
	}
	ids := make([]uuid.UUID, len(records))
	index := make(map[uuid.UUID]int, len(records))
	for i := range records {
		plans[i] = planFromRecord(records[i])
		ids[i] = records[i].ID
		index[records[i].ID] = i
	}
	var revisions []planRevisionRecord
	if err := db.gorm.WithContext(ctx).Where("plan_id IN ?", ids).Order("plan_id, revision DESC").Find(&revisions).Error; err != nil {
		return nil, fmt.Errorf("load Captain plan revisions: %w", err)
	}
	for _, record := range revisions {
		i := index[record.PlanID]
		revision := revisionFromRecord(record)
		if plans[i].LatestRevision == nil {
			latest := revision
			plans[i].LatestRevision = &latest
		}
		if plans[i].ApprovedRevisionID != nil && record.ID == *plans[i].ApprovedRevisionID {
			approved := revision
			plans[i].ApprovedRevision = &approved
		}
	}
	return plans, nil
}

func (db *DB) requireGorm() error {
	if db == nil || db.gorm == nil {
		return errors.New("Captain database is not initialized")
	}
	return nil
}

func planFromRecord(record planRecord) Plan {
	return Plan{
		ID: record.ID, SourceSessionID: record.SourceSessionID,
		SourcePromptRunID: record.SourcePromptRunID, SourceIterationID: record.SourceIterationID,
		SourceTurnID: record.SourceTurnID, Title: optionalString(record.Title), Slug: optionalString(record.Slug),
		Path: optionalString(record.Path), Variant: optionalString(record.Variant), SpecProfile: optionalString(record.SpecProfile),
		ApprovalState: record.ApprovalState, ApprovedRevisionID: record.ApprovedRevisionID,
		ApprovalComment: optionalString(record.ApprovalComment), ApprovedBy: optionalString(record.ApprovedBy),
		ApprovalCreatedAt: record.ApprovalCreatedAt, FeedbackAt: record.FeedbackAt,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func revisionFromRecord(record planRevisionRecord) PlanRevision {
	return PlanRevision{
		ID: record.ID, PlanID: record.PlanID, Revision: record.Revision,
		PlanMarkdown: record.PlanMarkdown, ContentHash: record.ContentHash,
		Feedback: optionalString(record.Feedback), CreatedBy: optionalString(record.CreatedBy), CreatedAt: record.CreatedAt,
	}
}

func normalizePlanMarkdown(markdown string) string {
	markdown = strings.ReplaceAll(markdown, "\r\n", "\n")
	markdown = strings.ReplaceAll(markdown, "\r", "\n")
	return strings.TrimSpace(markdown)
}

func planContentHash(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

func nullableTrimmed(value string) *string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return &value
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func equalOptionalString(left, right *string) bool {
	return optionalString(left) == optionalString(right)
}

func validPlanApprovalState(value PlanApprovalState) bool {
	switch value {
	case PlanApprovalPending, PlanApprovalApproved, PlanApprovalRejected, PlanApprovalRevisionRequested:
		return true
	default:
		return false
	}
}
