package database

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
	"gorm.io/gorm/clause"
)

// PromptRunIterationState mirrors the captain_prompt_run_iteration_state enum.
type PromptRunIterationState string

const (
	PromptRunIterationStatePending   PromptRunIterationState = "pending"
	PromptRunIterationStateRunning   PromptRunIterationState = "running"
	PromptRunIterationStateSucceeded PromptRunIterationState = "succeeded"
	PromptRunIterationStateFailed    PromptRunIterationState = "failed"
	PromptRunIterationStateCancelled PromptRunIterationState = "cancelled"
)

func validPromptRunIterationState(value PromptRunIterationState) bool {
	switch value {
	case PromptRunIterationStatePending, PromptRunIterationStateRunning, PromptRunIterationStateSucceeded,
		PromptRunIterationStateFailed, PromptRunIterationStateCancelled:
		return true
	default:
		return false
	}
}

// PromptRunIteration is one attempt of a prompt run: what was asked, what the
// verifier concluded, and the feedback carried into the next attempt.
type PromptRunIteration struct {
	ID                 uuid.UUID               `json:"id"`
	PromptRunID        uuid.UUID               `json:"promptRunId"`
	Iteration          int                     `json:"iteration"`
	State              PromptRunIterationState `json:"state"`
	Request            map[string]any          `json:"request,omitempty"`
	Feedback           string                  `json:"feedback,omitempty"`
	VerificationResult *api.VerifyReport       `json:"verificationResult,omitempty"`
	Error              string                  `json:"error,omitempty"`
	StartedAt          *time.Time              `json:"startedAt,omitempty"`
	FinishedAt         *time.Time              `json:"finishedAt,omitempty"`
	CreatedAt          time.Time               `json:"createdAt"`
	UpdatedAt          time.Time               `json:"updatedAt"`
}

// UpsertPromptRunIterationInput is the mutable state of one iteration. State,
// feedback, verification result and error belong to the newest write, which is
// what makes a crashed loop's replay converge instead of accumulating stale
// verdicts. Request and the timestamps are written only when supplied: a replay
// that omits them leaves what the row (and the state trigger) already holds.
type UpsertPromptRunIterationInput struct {
	PromptRunID uuid.UUID
	// Iteration is the 1-based loop turn ("iteration 1 of 3"), matching
	// captain_prompt_runs.current_iteration. A loop that indexes its turns from
	// zero converts before calling; the store rejects anything below 1.
	Iteration int
	State     PromptRunIterationState
	Request   map[string]any
	// Feedback, VerificationResult and Error are last-write-wins: a replay is a
	// full statement of the iteration, so passing a nil VerificationResult (or an
	// empty Feedback/Error) CLEARS what the row already holds rather than leaving
	// it. Restate the verdict on every replay that still stands behind it.
	Feedback           string
	VerificationResult *api.VerifyReport
	Error              string
	StartedAt          *time.Time
	FinishedAt         *time.Time
}

type promptRunIterationRecord struct {
	ID                 uuid.UUID               `gorm:"column:id;type:uuid;primaryKey"`
	PromptRunID        uuid.UUID               `gorm:"column:prompt_run_id;type:uuid"`
	Iteration          int                     `gorm:"column:iteration"`
	State              PromptRunIterationState `gorm:"column:state"`
	Request            map[string]any          `gorm:"column:request;serializer:json;type:jsonb"`
	Feedback           *string                 `gorm:"column:feedback"`
	VerificationResult *api.VerifyReport       `gorm:"column:verification_result;serializer:json;type:jsonb"`
	Error              *string                 `gorm:"column:error"`
	StartedAt          *time.Time              `gorm:"column:started_at"`
	FinishedAt         *time.Time              `gorm:"column:finished_at"`
	CreatedAt          time.Time               `gorm:"column:created_at"`
	UpdatedAt          time.Time               `gorm:"column:updated_at"`
}

func (promptRunIterationRecord) TableName() string { return "captain_prompt_run_iterations" }

// UpsertPromptRunIteration writes one iteration, keyed on (prompt_run_id,
// iteration) so a retried or resumed loop converges on a single row.
func (db *DB) UpsertPromptRunIteration(ctx context.Context, input UpsertPromptRunIterationInput) (PromptRunIteration, error) {
	if err := db.requireGorm(); err != nil {
		return PromptRunIteration{}, err
	}
	if err := input.validate(); err != nil {
		return PromptRunIteration{}, err
	}
	record := promptRunIterationRecord{
		ID: uuid.New(), PromptRunID: input.PromptRunID, Iteration: input.Iteration, State: input.State,
		Request: input.Request, Feedback: nullableTrimmed(input.Feedback), VerificationResult: input.VerificationResult,
		Error: nullableTrimmed(input.Error), StartedAt: input.StartedAt, FinishedAt: input.FinishedAt,
	}
	if record.Request == nil {
		record.Request = map[string]any{}
	}
	err := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "prompt_run_id"}, {Name: "iteration"}},
		DoUpdates: clause.Assignments(promptRunIterationConflictAssignments(input)),
	}).Create(&record).Error
	if err != nil {
		return PromptRunIteration{}, fmt.Errorf("upsert iteration %d of Captain prompt run %s: %w",
			input.Iteration, input.PromptRunID, err)
	}
	var stored promptRunIterationRecord
	err = db.gorm.WithContext(ctx).
		First(&stored, "prompt_run_id = ? AND iteration = ?", input.PromptRunID, input.Iteration).Error
	if err != nil {
		return PromptRunIteration{}, fmt.Errorf("read iteration %d of Captain prompt run %s: %w",
			input.Iteration, input.PromptRunID, err)
	}
	return promptRunIterationFromRecord(stored), nil
}

func (input UpsertPromptRunIterationInput) validate() error {
	if input.PromptRunID == uuid.Nil {
		return fmt.Errorf("%w: iteration requires a prompt run ID", ErrInvalidPromptRun)
	}
	if input.Iteration < 1 {
		return fmt.Errorf("%w: iteration %d is out of range; iterations are 1-based",
			ErrInvalidPromptRun, input.Iteration)
	}
	if !validPromptRunIterationState(input.State) {
		return fmt.Errorf("%w: unknown iteration state %q", ErrInvalidPromptRun, input.State)
	}
	if input.VerificationResult != nil {
		// A report carries the turn it judged. An unstamped (zero) report inherits
		// this row; one stamped for another turn is not this row's verdict.
		if stamped := input.VerificationResult.Iteration; stamped != 0 && stamped != input.Iteration {
			return fmt.Errorf("%w: verification result is stamped for iteration %d, cannot store it on iteration %d",
				ErrInvalidPromptRun, stamped, input.Iteration)
		}
		if err := input.VerificationResult.Validate(); err != nil {
			return fmt.Errorf("%w: iteration %d verification result: %v", ErrInvalidPromptRun, input.Iteration, err)
		}
	}
	if input.StartedAt != nil && input.FinishedAt != nil && input.FinishedAt.Before(*input.StartedAt) {
		return fmt.Errorf("%w: iteration %d finished at %s, before it started at %s",
			ErrInvalidPromptRun, input.Iteration, input.FinishedAt.UTC(), input.StartedAt.UTC())
	}
	return nil
}

// promptRunIterationConflictAssignments is what a replay of an iteration
// overwrites on the row already there.
func promptRunIterationConflictAssignments(input UpsertPromptRunIterationInput) map[string]any {
	assignments := map[string]any{
		"state":               excludedValue("state"),
		"feedback":            excludedValue("feedback"),
		"verification_result": excludedValue("verification_result"),
		"error":               excludedValue("error"),
		"updated_at":          clause.Expr{SQL: "now()"},
	}
	// captain_set_prompt_iteration_state fires BEFORE INSERT OR UPDATE on the
	// proposed row and derives started_at/finished_at from its state, so `excluded`
	// never carries a NULL timestamp to fall back from — and it is the UPDATE half
	// that back-fills finished_at when a terminal replay omits it. Assign only what
	// the caller supplied and let the existing row plus that trigger own the rest.
	if input.Request != nil {
		assignments["request"] = excludedValue("request")
	}
	if input.StartedAt != nil {
		assignments["started_at"] = excludedValue("started_at")
	}
	if input.FinishedAt != nil {
		assignments["finished_at"] = excludedValue("finished_at")
	}
	return assignments
}

func excludedValue(column string) clause.Expr {
	return clause.Expr{SQL: "excluded." + column}
}

// ListPromptRunIterations returns a run's attempts in attempt order.
func (db *DB) ListPromptRunIterations(ctx context.Context, runID uuid.UUID) ([]PromptRunIteration, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if runID == uuid.Nil {
		return nil, fmt.Errorf("%w: prompt run ID is required", ErrInvalidPromptRun)
	}
	var records []promptRunIterationRecord
	err := db.gorm.WithContext(ctx).Where("prompt_run_id = ?", runID).Order("iteration ASC").Find(&records).Error
	if err != nil {
		return nil, fmt.Errorf("list iterations of Captain prompt run %s: %w", runID, err)
	}
	iterations := make([]PromptRunIteration, len(records))
	for i := range records {
		iterations[i] = promptRunIterationFromRecord(records[i])
	}
	return iterations, nil
}

// LatestPromptRunVerification returns the newest report a run actually produced
// and the iteration that produced it. The newest iteration is often still
// running and carries no verdict, so the newest *report* is not the newest row.
// A run that has never been verified yields (nil, 0, nil): iterations are
// 1-based, so iteration 0 is never a real turn and the zero is unambiguous.
func (db *DB) LatestPromptRunVerification(ctx context.Context, runID uuid.UUID) (*api.VerifyReport, int, error) {
	if runID == uuid.Nil {
		return nil, 0, fmt.Errorf("%w: prompt run ID is required", ErrInvalidPromptRun)
	}
	latest, err := db.latestPromptRunVerifications(ctx, []uuid.UUID{runID})
	if err != nil {
		return nil, 0, err
	}
	found, ok := latest[runID]
	if !ok {
		return nil, 0, nil
	}
	return found.Report, found.Iteration, nil
}

type promptRunVerification struct {
	Iteration int
	Report    *api.VerifyReport
}

type promptRunVerificationRecord struct {
	PromptRunID        uuid.UUID       `gorm:"column:prompt_run_id"`
	Iteration          int             `gorm:"column:iteration"`
	VerificationResult json.RawMessage `gorm:"column:verification_result"`
}

// latestPromptRunVerifications resolves the newest report per run for a whole
// batch of runs in one DISTINCT ON query, so a listing never fans out per row.
func (db *DB) latestPromptRunVerifications(ctx context.Context, runIDs []uuid.UUID) (map[uuid.UUID]promptRunVerification, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if len(runIDs) == 0 {
		return map[uuid.UUID]promptRunVerification{}, nil
	}
	var records []promptRunVerificationRecord
	// jsonb_typeof excludes a stored JSON `null`, which passes IS NOT NULL and
	// would otherwise decode into a zero-valued report that reads as a blank pass.
	err := db.gorm.WithContext(ctx).Model(&promptRunIterationRecord{}).
		Select("DISTINCT ON (prompt_run_id) prompt_run_id, iteration, verification_result").
		Where("prompt_run_id IN ? AND verification_result IS NOT NULL AND jsonb_typeof(verification_result) <> 'null'", runIDs).
		Order("prompt_run_id, iteration DESC").
		Scan(&records).Error
	if err != nil {
		return nil, fmt.Errorf("read latest verification of %d Captain prompt run(s): %w", len(runIDs), err)
	}
	latest := make(map[uuid.UUID]promptRunVerification, len(records))
	for _, record := range records {
		var report api.VerifyReport
		if err := json.Unmarshal(record.VerificationResult, &report); err != nil {
			return nil, fmt.Errorf("decode verification result of Captain prompt run %s iteration %d: %w",
				record.PromptRunID, record.Iteration, err)
		}
		if err := report.Validate(); err != nil {
			return nil, fmt.Errorf("verification result of Captain prompt run %s iteration %d is corrupt: %w",
				record.PromptRunID, record.Iteration, err)
		}
		latest[record.PromptRunID] = promptRunVerification{Iteration: record.Iteration, Report: &report}
	}
	return latest, nil
}

func promptRunIterationFromRecord(record promptRunIterationRecord) PromptRunIteration {
	return PromptRunIteration{
		ID: record.ID, PromptRunID: record.PromptRunID, Iteration: record.Iteration, State: record.State,
		Request: record.Request, Feedback: optionalString(record.Feedback),
		VerificationResult: record.VerificationResult, Error: optionalString(record.Error),
		StartedAt: record.StartedAt, FinishedAt: record.FinishedAt,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}
