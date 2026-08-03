package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (db *DB) CreatePromptRun(ctx context.Context, input CreatePromptRunInput) (*PromptRun, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if input.SessionID == uuid.Nil {
		return nil, fmt.Errorf("%w: session ID is required", ErrInvalidPromptRun)
	}
	if input.InputPlanRevisionID != nil && input.InputPlanID == nil {
		return nil, fmt.Errorf("%w: input revision requires an input plan", ErrInvalidPromptRun)
	}
	session, err := db.GetSession(ctx, input.SessionID)
	if err != nil {
		return nil, err
	}
	if input.TurnID != nil {
		var turn turnRecord
		if err := db.gorm.WithContext(ctx).First(&turn, "id = ?", *input.TurnID).Error; err != nil {
			return nil, fmt.Errorf("read Captain prompt run turn: %w", err)
		}
		if turn.SessionID != input.SessionID {
			return nil, fmt.Errorf("%w: turn %s belongs to session %s", ErrPromptRunConflict, turn.ID, turn.SessionID)
		}
	}
	callerSuppliedID := input.ID != uuid.Nil
	if !callerSuppliedID {
		input.ID = uuid.New()
	}
	rootID := session.ID
	if session.RootSessionID != nil {
		rootID = *session.RootSessionID
	}
	if input.RootSessionID != nil {
		if *input.RootSessionID != rootID {
			return nil, fmt.Errorf("%w: root session %s does not match session aggregate root %s", ErrInvalidPromptRun, *input.RootSessionID, rootID)
		}
	}
	if input.ExecutionSessionID != nil {
		if err := db.validateExecutionSession(ctx, session, *input.ExecutionSessionID); err != nil {
			return nil, err
		}
	}
	if input.ParentRunID != nil {
		if *input.ParentRunID == uuid.Nil || *input.ParentRunID == input.ID {
			return nil, fmt.Errorf("%w: parent run cannot be empty or self", ErrInvalidPromptRun)
		}
		parent, err := db.GetPromptRun(ctx, *input.ParentRunID)
		if err != nil {
			return nil, err
		}
		if parent.RootSessionID != rootID {
			return nil, fmt.Errorf("%w: parent run %s belongs to root %s, not %s", ErrPromptRunConflict, parent.ID, parent.RootSessionID, rootID)
		}
	}
	now := time.Now().UTC()
	record := promptRunRecord{
		ID: input.ID, SessionID: input.SessionID, TurnID: input.TurnID, RootSessionID: rootID, ExecutionSessionID: input.ExecutionSessionID, BatchID: input.BatchID,
		ParentRunID: input.ParentRunID, InputPlanID: input.InputPlanID, InputPlanRevisionID: input.InputPlanRevisionID,
		Origin: nullableTrimmed(input.Origin), SpecProfile: nullableTrimmed(input.SpecProfile),
		AdmissionKey: nullableTrimmed(input.AdmissionKey), RenderedSpec: input.RenderedSpec, Runtime: input.Runtime,
		PromptMarkdown: nullableTrimmed(input.PromptMarkdown), VerificationMarkdown: nullableTrimmed(input.VerificationMarkdown),
		Phase: PromptRunPhaseQueued, State: PromptRunStatePending, QueuedAt: now,
	}
	if record.RenderedSpec == nil {
		record.RenderedSpec = map[string]any{}
	}
	result := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return nil, fmt.Errorf("create Captain prompt run: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return db.GetPromptRun(ctx, record.ID)
	}
	var existing promptRunRecord
	query := db.gorm.WithContext(ctx)
	if record.AdmissionKey != nil {
		query = query.Where("admission_key = ?", *record.AdmissionKey)
	} else {
		query = query.Where("id = ?", record.ID)
	}
	if err := query.First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: another active run exists for root session %s", ErrPromptRunConflict, rootID)
		}
		return nil, fmt.Errorf("read existing Captain prompt run: %w", err)
	}
	if existing.SessionID != input.SessionID {
		return nil, fmt.Errorf("%w: existing run belongs to session %s", ErrPromptRunConflict, existing.SessionID)
	}
	if callerSuppliedID && existing.ID != input.ID {
		return nil, fmt.Errorf("%w: admission identity already belongs to run %s, not caller-supplied ID %s", ErrPromptRunConflict, existing.ID, input.ID)
	}
	return db.GetPromptRun(ctx, existing.ID)
}

func (db *DB) GetPromptRun(ctx context.Context, id uuid.UUID) (*PromptRun, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var record promptRunRecord
	if err := db.gorm.WithContext(ctx).First(&record, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrPromptRunNotFound, id)
		}
		return nil, fmt.Errorf("get Captain prompt run: %w", err)
	}
	out := promptRunFromRecord(record)
	return &out, nil
}

// ListPromptRuns returns prompt runs newest-first. The UUID tie-breaker keeps
// ordering stable when several runs share the same queued timestamp.
func (db *DB) ListPromptRuns(ctx context.Context, filter PromptRunFilter) ([]PromptRun, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	query := db.gorm.WithContext(ctx).Order("queued_at DESC, id DESC")
	if filter.SessionID != nil {
		if *filter.SessionID == uuid.Nil {
			return nil, fmt.Errorf("%w: session ID filter cannot be empty", ErrInvalidPromptRun)
		}
		query = query.Where("session_id = ?", *filter.SessionID)
	}
	if filter.State != nil {
		if !validPromptRunState(*filter.State) {
			return nil, fmt.Errorf("%w: unknown state %q", ErrInvalidPromptRun, *filter.State)
		}
		query = query.Where("state = ?", *filter.State)
	}
	var records []promptRunRecord
	if err := query.Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list Captain prompt runs: %w", err)
	}
	runs := make([]PromptRun, len(records))
	for i := range records {
		runs[i] = promptRunFromRecord(records[i])
	}
	return runs, nil
}

// UpdatePromptRun applies phase/state/result changes only when ExpectedVersion
// matches. Captain's trigger advances Version exactly when durable state changes.
func (db *DB) UpdatePromptRun(ctx context.Context, input UpdatePromptRunInput) (*PromptRun, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if input.ID == uuid.Nil || input.ExpectedVersion < 0 {
		return nil, fmt.Errorf("%w: ID and a nonnegative expected version are required", ErrInvalidPromptRun)
	}
	updates := map[string]any{}
	var distinctPredicates []string
	var distinctArgs []any
	if input.Phase != nil {
		if !validPromptRunPhase(*input.Phase) {
			return nil, fmt.Errorf("%w: unknown phase %q", ErrInvalidPromptRun, *input.Phase)
		}
		updates["phase"] = *input.Phase
		distinctPredicates = append(distinctPredicates, "phase IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, *input.Phase)
	}
	if input.State != nil {
		if !validPromptRunState(*input.State) {
			return nil, fmt.Errorf("%w: unknown state %q", ErrInvalidPromptRun, *input.State)
		}
		updates["state"] = *input.State
		distinctPredicates = append(distinctPredicates, "state IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, *input.State)
	}
	if input.CurrentIteration != nil {
		if *input.CurrentIteration < 0 {
			return nil, fmt.Errorf("%w: current iteration cannot be negative", ErrInvalidPromptRun)
		}
		updates["current_iteration"] = *input.CurrentIteration
		distinctPredicates = append(distinctPredicates, "current_iteration IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, *input.CurrentIteration)
	}
	if input.ExecutionSessionID != nil {
		var record promptRunRecord
		if err := db.gorm.WithContext(ctx).First(&record, "id = ?", input.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("%w: %s", ErrPromptRunNotFound, input.ID)
			}
			return nil, fmt.Errorf("read Captain prompt run execution binding: %w", err)
		}
		admission, err := db.GetSession(ctx, record.SessionID)
		if err != nil {
			return nil, err
		}
		if err := db.validateExecutionSession(ctx, admission, *input.ExecutionSessionID); err != nil {
			return nil, err
		}
		updates["execution_session_id"] = *input.ExecutionSessionID
		distinctPredicates = append(distinctPredicates, "execution_session_id IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, *input.ExecutionSessionID)
	}
	if input.RenderedSpec != nil {
		if *input.RenderedSpec == nil {
			return nil, fmt.Errorf("%w: rendered spec cannot be null", ErrInvalidPromptRun)
		}
		encoded, err := json.Marshal(*input.RenderedSpec)
		if err != nil {
			return nil, fmt.Errorf("%w: encode rendered spec: %v", ErrInvalidPromptRun, err)
		}
		updates["rendered_spec"] = *input.RenderedSpec
		distinctPredicates = append(distinctPredicates, "rendered_spec IS DISTINCT FROM CAST(? AS jsonb)")
		distinctArgs = append(distinctArgs, string(encoded))
	}
	if input.Runtime != nil {
		encoded, err := json.Marshal(*input.Runtime)
		if err != nil {
			return nil, fmt.Errorf("%w: encode runtime: %v", ErrInvalidPromptRun, err)
		}
		updates["runtime"] = *input.Runtime
		distinctPredicates = append(distinctPredicates, "runtime IS DISTINCT FROM ?::jsonb")
		distinctArgs = append(distinctArgs, string(encoded))
	}
	if input.ResultText != nil {
		updates["result_text"] = *input.ResultText
		distinctPredicates = append(distinctPredicates, "result_text IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, *input.ResultText)
	}
	if input.ResultJSON != nil {
		if *input.ResultJSON == nil {
			updates["result_json"] = nil
			distinctPredicates = append(distinctPredicates, "result_json IS NOT NULL")
		} else {
			encoded, err := json.Marshal(*input.ResultJSON)
			if err != nil {
				return nil, fmt.Errorf("%w: encode result JSON: %v", ErrInvalidPromptRun, err)
			}
			updates["result_json"] = *input.ResultJSON
			distinctPredicates = append(distinctPredicates, "result_json IS DISTINCT FROM CAST(? AS jsonb)")
			distinctArgs = append(distinctArgs, string(encoded))
		}
	}
	if input.ApprovalState != nil {
		state := *input.ApprovalState
		state.ProviderCheckpoint = nil
		if err := state.Validate(); err != nil {
			return nil, fmt.Errorf("%w: approval state: %v", ErrInvalidPromptRun, err)
		}
		encoded, err := json.Marshal(state)
		if err != nil {
			return nil, fmt.Errorf("%w: encode approval state: %v", ErrInvalidPromptRun, err)
		}
		updates["approval_state"] = &state
		distinctPredicates = append(distinctPredicates, "approval_state IS DISTINCT FROM CAST(? AS jsonb)")
		distinctArgs = append(distinctArgs, string(encoded))
	} else if input.ClearApprovalState {
		updates["approval_state"] = nil
		distinctPredicates = append(distinctPredicates, "approval_state IS NOT NULL")
	}
	if input.ProviderCheckpoint != nil {
		checkpoint := input.ProviderCheckpoint
		if strings.TrimSpace(checkpoint.Codec) == "" || checkpoint.Version <= 0 || len(checkpoint.Payload) == 0 {
			return nil, fmt.Errorf("%w: provider checkpoint requires a codec, positive version, and payload", ErrInvalidPromptRun)
		}
		updates["provider_checkpoint_codec"] = strings.TrimSpace(checkpoint.Codec)
		updates["provider_checkpoint_version"] = checkpoint.Version
		updates["provider_checkpoint"] = append([]byte(nil), checkpoint.Payload...)
		distinctPredicates = append(distinctPredicates,
			"provider_checkpoint_codec IS DISTINCT FROM ? OR provider_checkpoint_version IS DISTINCT FROM ? OR provider_checkpoint IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, strings.TrimSpace(checkpoint.Codec), checkpoint.Version, checkpoint.Payload)
	} else if input.ClearProviderCheckpoint {
		updates["provider_checkpoint_codec"] = nil
		updates["provider_checkpoint_version"] = nil
		updates["provider_checkpoint"] = nil
		distinctPredicates = append(distinctPredicates,
			"provider_checkpoint_codec IS NOT NULL OR provider_checkpoint_version IS NOT NULL OR provider_checkpoint IS NOT NULL")
	}
	if input.Error != nil {
		updates["error"] = *input.Error
		distinctPredicates = append(distinctPredicates, "error IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, *input.Error)
	}
	if len(updates) == 0 {
		return nil, fmt.Errorf("%w: no update fields supplied", ErrInvalidPromptRun)
	}
	query := db.gorm.WithContext(ctx).Model(&promptRunRecord{}).
		Where("id = ? AND version = ?", input.ID, input.ExpectedVersion).
		Where("("+strings.Join(distinctPredicates, " OR ")+")", distinctArgs...)
	if input.ExecutionSessionID != nil {
		query = query.Where("(execution_session_id IS NULL OR execution_session_id = ?)", *input.ExecutionSessionID)
	}
	result := query.Updates(updates)
	if result.Error != nil {
		return nil, fmt.Errorf("update Captain prompt run: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		var current promptRunRecord
		if err := db.gorm.WithContext(ctx).First(&current, "id = ?", input.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("%w: %s", ErrPromptRunNotFound, input.ID)
			}
			return nil, fmt.Errorf("check Captain prompt run: %w", err)
		}
		if current.Version != input.ExpectedVersion {
			return nil, fmt.Errorf("%w: prompt run %s is no longer at version %d", ErrPromptRunConflict, input.ID, input.ExpectedVersion)
		}
		if input.ExecutionSessionID != nil && current.ExecutionSessionID != nil && *current.ExecutionSessionID != *input.ExecutionSessionID {
			return nil, fmt.Errorf("%w: prompt run %s is already bound to execution session %s", ErrPromptRunConflict, input.ID, *current.ExecutionSessionID)
		}
		out := promptRunFromRecord(current)
		return &out, nil
	}
	return db.GetPromptRun(ctx, input.ID)
}

func promptRunFromRecord(record promptRunRecord) PromptRun {
	var checkpoint *PromptRunCheckpoint
	if record.ProviderCheckpointCodec != nil || record.ProviderCheckpointVersion != nil || len(record.ProviderCheckpoint) > 0 {
		if record.ProviderCheckpointCodec != nil && record.ProviderCheckpointVersion != nil && len(record.ProviderCheckpoint) > 0 {
			checkpoint = &PromptRunCheckpoint{
				Codec: *record.ProviderCheckpointCodec, Version: *record.ProviderCheckpointVersion,
				Payload: append([]byte(nil), record.ProviderCheckpoint...),
			}
		}
	}
	return PromptRun{
		ID: record.ID, SessionID: record.SessionID, TurnID: record.TurnID, RootSessionID: record.RootSessionID, ExecutionSessionID: record.ExecutionSessionID, BatchID: record.BatchID,
		ParentRunID: record.ParentRunID, InputPlanID: record.InputPlanID, InputPlanRevisionID: record.InputPlanRevisionID,
		Origin: optionalString(record.Origin), SpecProfile: optionalString(record.SpecProfile), AdmissionKey: optionalString(record.AdmissionKey),
		RenderedSpec: record.RenderedSpec, Runtime: record.Runtime, PromptMarkdown: optionalString(record.PromptMarkdown),
		VerificationMarkdown: optionalString(record.VerificationMarkdown), Phase: record.Phase, State: record.State,
		CurrentIteration: record.CurrentIteration, ResultText: optionalString(record.ResultText), ResultJSON: record.ResultJSON,
		ApprovalState: record.ApprovalState, ProviderCheckpoint: checkpoint,
		Error: optionalString(record.Error), Version: record.Version, QueuedAt: record.QueuedAt, StartedAt: record.StartedAt,
		FinishedAt: record.FinishedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func (db *DB) validateExecutionSession(ctx context.Context, admission *Session, executionID uuid.UUID) error {
	if admission == nil || executionID == uuid.Nil {
		return fmt.Errorf("%w: execution session ID is required", ErrInvalidPromptRun)
	}
	execution, err := db.GetSession(ctx, executionID)
	if err != nil {
		return err
	}
	if (execution.ParentSessionID != nil || execution.RootSessionID != nil) &&
		sessionAggregateRoot(execution) != sessionAggregateRoot(admission) {
		return fmt.Errorf("%w: execution session %s belongs to root %s, not %s",
			ErrPromptRunConflict, execution.ID, sessionAggregateRoot(execution), sessionAggregateRoot(admission))
	}
	if execution.Source != "claude" && execution.Source != "codex" {
		return fmt.Errorf("%w: execution session %s has unsupported source %q", ErrPromptRunConflict, execution.ID, execution.Source)
	}
	if strings.TrimSpace(admission.ProviderSessionID) == "" || admission.ProviderSessionID != execution.ProviderSessionID {
		return fmt.Errorf("%w: execution session %s provider identity does not match admission session %s", ErrPromptRunConflict, execution.ID, admission.ID)
	}
	return nil
}

func validPromptRunPhase(value PromptRunPhase) bool {
	switch value {
	case PromptRunPhaseQueued, PromptRunPhasePreRun, PromptRunPhaseGenerate, PromptRunPhaseVerify,
		PromptRunPhaseFeedback, PromptRunPhasePostRun, PromptRunPhaseOutput, PromptRunPhaseFinished:
		return true
	default:
		return false
	}
}

func validPromptRunState(value PromptRunState) bool {
	switch value {
	case PromptRunStatePending, PromptRunStateRunning, PromptRunStateWaiting,
		PromptRunStateSucceeded, PromptRunStateFailed, PromptRunStateCancelled:
		return true
	default:
		return false
	}
}

func equalOptionalUUID(left, right *uuid.UUID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
