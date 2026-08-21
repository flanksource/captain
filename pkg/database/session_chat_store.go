package database

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrOpenChatTurn = errors.New("captain chat session already has an open turn")

const incompleteChatAdmissionReason = "chat execution admission did not complete"

type ChatTurn struct {
	ID             uuid.UUID
	SessionID      uuid.UUID
	ProviderTurnID string
	Index          int
	Status         TurnStatus
}

type CreateChatTurnInput struct {
	SessionID      uuid.UUID
	ProviderTurnID string
}

type RecoverIncompleteChatAdmissionInput struct {
	SessionID      uuid.UUID
	ProviderTurnID string
}

func (db *DB) CreateChatTurn(ctx context.Context, input CreateChatTurnInput) (*ChatTurn, bool, error) {
	if input.SessionID == uuid.Nil || strings.TrimSpace(input.ProviderTurnID) == "" {
		return nil, false, fmt.Errorf("%w: session and provider turn IDs are required", ErrInvalidIngest)
	}
	providerTurnID := strings.TrimSpace(input.ProviderTurnID)
	var output *ChatTurn
	created := false
	err := db.Transaction(ctx, func(tx *DB) error {
		if err := tx.gorm.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&sessionRecord{}, "id = ?", input.SessionID).Error; err != nil {
			return fmt.Errorf("lock Captain chat session: %w", err)
		}
		var existing turnRecord
		err := tx.gorm.WithContext(ctx).
			Where("session_id = ? AND provider_turn_id = ?", input.SessionID, providerTurnID).
			First(&existing).Error
		if err == nil {
			output = chatTurnFromRecord(existing)
			return nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read Captain chat turn: %w", err)
		}
		var open turnRecord
		err = tx.gorm.WithContext(ctx).
			Where("session_id = ? AND status = ?", input.SessionID, TurnStatusOpen).
			First(&open).Error
		if err == nil {
			return fmt.Errorf("%w: session %s has turn %s for provider turn %q",
				ErrOpenChatTurn, input.SessionID, open.ID, optionalString(open.ProviderTurnID))
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read open Captain chat turn: %w", err)
		}
		var next int
		if err := tx.gorm.WithContext(ctx).Model(&turnRecord{}).
			Where("session_id = ?", input.SessionID).
			Select("COALESCE(MAX(turn_index), -1) + 1").Scan(&next).Error; err != nil {
			return fmt.Errorf("select Captain chat turn index: %w", err)
		}
		now := time.Now().UTC()
		record := turnRecord{
			ID: uuid.New(), SessionID: input.SessionID, ProviderTurnID: &providerTurnID,
			TurnIndex: next, Status: TurnStatusOpen, StartedAt: &now,
		}
		if err := tx.gorm.WithContext(ctx).Create(&record).Error; err != nil {
			return fmt.Errorf("create Captain chat turn: %w", err)
		}
		output = chatTurnFromRecord(record)
		created = true
		return nil
	})
	return output, created, err
}

func (db *DB) RecoverIncompleteChatAdmission(ctx context.Context, input RecoverIncompleteChatAdmissionInput) (*ChatTurn, error) {
	input.ProviderTurnID = strings.TrimSpace(input.ProviderTurnID)
	if input.SessionID == uuid.Nil || input.ProviderTurnID == "" {
		return nil, fmt.Errorf("%w: session and provider turn IDs are required", ErrInvalidIngest)
	}
	var recovered *ChatTurn
	err := db.Transaction(ctx, func(tx *DB) error {
		if err := tx.gorm.WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&sessionRecord{}, "id = ?", input.SessionID).Error; err != nil {
			return fmt.Errorf("lock Captain chat session: %w", err)
		}
		var turn turnRecord
		if err := tx.gorm.WithContext(ctx).
			Where("session_id = ? AND status = ?", input.SessionID, TurnStatusOpen).
			First(&turn).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return fmt.Errorf("read open Captain chat turn: %w", err)
		}
		if optionalString(turn.ProviderTurnID) == input.ProviderTurnID {
			return nil
		}
		var runs []promptRunRecord
		if err := tx.gorm.WithContext(ctx).Where("turn_id = ?", turn.ID).Limit(2).Find(&runs).Error; err != nil {
			return fmt.Errorf("read open Captain chat turn prompt run: %w", err)
		}
		if len(runs) > 1 {
			return fmt.Errorf("open Captain chat turn %s has %d prompt runs", turn.ID, len(runs))
		}
		var modelCalls int64
		if err := tx.gorm.WithContext(ctx).Model(&modelCallRecord{}).
			Where("turn_id = ?", turn.ID).Count(&modelCalls).Error; err != nil {
			return fmt.Errorf("count open Captain chat turn model calls: %w", err)
		}
		if modelCalls > 0 || (len(runs) == 1 &&
			(runs[0].State != PromptRunStatePending || runs[0].Phase != PromptRunPhaseQueued)) {
			return nil
		}
		if len(runs) == 1 {
			phase := PromptRunPhaseFinished
			state := PromptRunStateFailed
			reason := incompleteChatAdmissionReason
			if _, err := tx.UpdatePromptRun(ctx, UpdatePromptRunInput{
				ID: runs[0].ID, ExpectedVersion: runs[0].Version,
				Phase: &phase, State: &state, Error: &reason,
			}); err != nil {
				return err
			}
		}
		if err := tx.FinishChatTurn(ctx, turn.ID, TurnStatusError, incompleteChatAdmissionReason); err != nil {
			return err
		}
		recovered = chatTurnFromRecord(turn)
		recovered.Status = TurnStatusError
		return nil
	})
	return recovered, err
}

type CreateChatModelCallInput struct {
	TurnID      uuid.UUID
	PromptRunID uuid.UUID
	Model       string
	Backend     string
	Effort      string
}

type UpdateChatModelCallRuntimeInput struct {
	ID      uuid.UUID
	Model   string
	Backend string
	Effort  string
}

type ModelCallStatus string

const (
	ModelCallStatusSucceeded ModelCallStatus = "succeeded"
	ModelCallStatusFailed    ModelCallStatus = "failed"
	ModelCallStatusCancelled ModelCallStatus = "cancelled"
)

type FinishChatModelCallInput struct {
	ID         uuid.UUID
	Status     ModelCallStatus
	StopReason string
	Event      api.Event
	// Cost is the priced breakdown for Event.Usage, built by the caller via
	// ai.PriceUsage. Nil when the call ended without usage (interrupt, error).
	Cost *api.Cost
	// ContextWindowTokens is the resolved model's context window, so context
	// occupancy is reportable server-side rather than only from the UI catalog.
	ContextWindowTokens int
}

func (db *DB) GetChatTurn(ctx context.Context, id uuid.UUID) (*ChatTurn, error) {
	var record turnRecord
	if err := db.gorm.WithContext(ctx).First(&record, "id = ?", id).Error; err != nil {
		return nil, fmt.Errorf("get Captain chat turn %s: %w", id, err)
	}
	return chatTurnFromRecord(record), nil
}

func (db *DB) CreateChatModelCall(ctx context.Context, input CreateChatModelCallInput) (uuid.UUID, error) {
	if input.TurnID == uuid.Nil || input.PromptRunID == uuid.Nil || strings.TrimSpace(input.Model) == "" || strings.TrimSpace(input.Backend) == "" {
		return uuid.Nil, fmt.Errorf("%w: turn, prompt run, model, and backend are required", ErrInvalidIngest)
	}
	var index int
	if err := db.gorm.WithContext(ctx).Model(&modelCallRecord{}).
		Where("turn_id = ?", input.TurnID).
		Select("COALESCE(MAX(call_index), -1) + 1").Scan(&index).Error; err != nil {
		return uuid.Nil, fmt.Errorf("select Captain model call index: %w", err)
	}
	now := time.Now().UTC()
	record := modelCallRecord{
		ID: uuid.New(), TurnID: input.TurnID, PromptRunID: &input.PromptRunID,
		CallIndex: index, Model: strings.TrimSpace(input.Model), Backend: strings.TrimSpace(input.Backend),
		Effort: nullableTrimmed(input.Effort), Status: "running", Currency: "USD", StartedAt: &now,
	}
	if err := db.gorm.WithContext(ctx).Create(&record).Error; err != nil {
		return uuid.Nil, fmt.Errorf("create Captain chat model call: %w", err)
	}
	return record.ID, nil
}

// UpdateChatModelCallRuntime records the candidate a fallback provider actually
// selected while the model call is still running.
func (db *DB) UpdateChatModelCallRuntime(ctx context.Context, input UpdateChatModelCallRuntimeInput) error {
	input.Model = strings.TrimSpace(input.Model)
	input.Backend = strings.TrimSpace(input.Backend)
	if input.ID == uuid.Nil || input.Model == "" || input.Backend == "" {
		return fmt.Errorf("%w: model call ID, model, and backend are required", ErrInvalidIngest)
	}
	result := db.gorm.WithContext(ctx).Model(&modelCallRecord{}).
		Where("id = ? AND status = 'running'", input.ID).
		Updates(map[string]any{
			"model": input.Model, "backend": input.Backend, "effort": nullableTrimmed(input.Effort),
		})
	if result.Error != nil {
		return fmt.Errorf("update Captain chat model call runtime: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("update Captain chat model call runtime %s: expected one running call, updated %d", input.ID, result.RowsAffected)
	}
	return nil
}

func (db *DB) FinishChatModelCall(ctx context.Context, input FinishChatModelCallInput) error {
	if input.ID == uuid.Nil || strings.TrimSpace(input.StopReason) == "" {
		return fmt.Errorf("%w: model call ID and stop reason are required", ErrInvalidIngest)
	}
	switch input.Status {
	case ModelCallStatusSucceeded, ModelCallStatusFailed, ModelCallStatusCancelled:
	default:
		return fmt.Errorf("%w: terminal model call status %q is invalid", ErrInvalidIngest, input.Status)
	}
	updates := map[string]any{
		"status": input.Status, "stop_reason": strings.TrimSpace(input.StopReason), "ended_at": time.Now().UTC(),
		"provider_cost_usd": input.Event.CostUSD,
	}
	if input.Cost != nil {
		updates["input_cost"] = input.Cost.InputCost
		updates["output_cost"] = input.Cost.OutputCost
		updates["reasoning_cost"] = input.Cost.ReasoningCost
		updates["cache_read_cost"] = input.Cost.CacheReadCost
		updates["cache_write_cost"] = input.Cost.CacheWriteCost
	}
	if input.ContextWindowTokens > 0 {
		updates["context_window_tokens"] = input.ContextWindowTokens
	}
	if input.Event.Usage != nil {
		updates["input_tokens"] = input.Event.Usage.InputTokens
		updates["output_tokens"] = input.Event.Usage.OutputTokens
		updates["reasoning_tokens"] = input.Event.Usage.ReasoningTokens
		updates["cache_read_tokens"] = input.Event.Usage.CacheReadTokens
		updates["cache_write_tokens"] = input.Event.Usage.CacheWriteTokens
		// Context occupancy is the whole prompt, and the buckets are disjoint
		// (pkg/api/cost.go): cache reads are context too. Counting InputTokens
		// alone reports "5 / 1,000,000" for a 115K-token claude-agent turn.
		updates["context_tokens"] = input.Event.Usage.InputTokens +
			input.Event.Usage.CacheReadTokens + input.Event.Usage.CacheWriteTokens
	}
	if input.Event.Error != "" {
		updates["error"] = input.Event.Error
	}
	result := db.gorm.WithContext(ctx).Model(&modelCallRecord{}).
		Where("id = ? AND status = 'running'", input.ID).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("finish Captain chat model call: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("finish Captain chat model call %s: expected one running call, updated %d", input.ID, result.RowsAffected)
	}
	return nil
}

func (db *DB) FinishChatTurn(ctx context.Context, id uuid.UUID, status TurnStatus, reason string) error {
	if status != TurnStatusEnded && status != TurnStatusError && status != TurnStatusInterrupted {
		return fmt.Errorf("%w: terminal chat turn status %q is invalid", ErrInvalidIngest, status)
	}
	updates := map[string]any{"status": status, "ended_at": time.Now().UTC()}
	if strings.TrimSpace(reason) != "" {
		if status == TurnStatusError {
			updates["error"] = strings.TrimSpace(reason)
		} else {
			updates["stop_reason"] = strings.TrimSpace(reason)
		}
	}
	result := db.gorm.WithContext(ctx).Model(&turnRecord{}).Where("id = ? AND status = 'open'", id).Updates(updates)
	if result.Error != nil {
		return fmt.Errorf("finish Captain chat turn: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("finish Captain chat turn %s: expected one open turn, updated %d", id, result.RowsAffected)
	}
	return nil
}

type PutChatMessageInput struct {
	SessionID uuid.UUID
	// TurnID is zero for a persisted conversation seed and nonzero for real
	// user/assistant turn messages.
	TurnID            uuid.UUID
	ProviderMessageID string
	Role              string
	Parts             json.RawMessage
	Replace           bool
}

func (db *DB) PutChatMessage(ctx context.Context, input PutChatMessageInput) error {
	input.ProviderMessageID = strings.TrimSpace(input.ProviderMessageID)
	input.Role = strings.TrimSpace(input.Role)
	if input.SessionID == uuid.Nil || input.ProviderMessageID == "" || input.Role == "" || !json.Valid(input.Parts) {
		return fmt.Errorf("%w: session, message ID, role, and valid parts are required", ErrInvalidIngest)
	}
	var turnID *uuid.UUID
	if input.TurnID != uuid.Nil {
		turnID = &input.TurnID
	}
	var existing messageRecord
	err := db.gorm.WithContext(ctx).
		Where("session_id = ? AND provider_message_id = ?", input.SessionID, input.ProviderMessageID).
		First(&existing).Error
	if err == nil {
		if !input.Replace {
			if existing.Role != input.Role || !bytes.Equal(bytes.TrimSpace(existing.Parts), bytes.TrimSpace(input.Parts)) {
				return fmt.Errorf("captain chat message %q was replayed with different content", input.ProviderMessageID)
			}
			return nil
		}
		if err := db.gorm.WithContext(ctx).Model(&messageRecord{}).Where("id = ?", existing.ID).
			Updates(map[string]any{"role": input.Role, "parts": append(json.RawMessage(nil), input.Parts...), "turn_id": turnID}).Error; err != nil {
			return fmt.Errorf("replace Captain chat message %q: %w", input.ProviderMessageID, err)
		}
		return db.touchChatSession(ctx, input.SessionID)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("read Captain chat message: %w", err)
	}
	if input.Replace {
		return fmt.Errorf("captain chat message %q cannot be replaced because it does not exist", input.ProviderMessageID)
	}
	var sequence int64
	if err := db.gorm.WithContext(ctx).Model(&messageRecord{}).Where("session_id = ?", input.SessionID).
		Select("COALESCE(MAX(sequence), -1) + 1").Scan(&sequence).Error; err != nil {
		return fmt.Errorf("select Captain chat message sequence: %w", err)
	}
	now := time.Now().UTC()
	record := messageRecord{
		ID: uuid.New(), SessionID: input.SessionID, TurnID: turnID,
		ProviderMessageID: &input.ProviderMessageID, Sequence: sequence, Role: input.Role,
		Parts: append([]byte(nil), input.Parts...), OccurredAt: &now,
	}
	if err := db.gorm.WithContext(ctx).Create(&record).Error; err != nil {
		return fmt.Errorf("create Captain chat message: %w", err)
	}
	return db.touchChatSession(ctx, input.SessionID)
}

type ForkChatSessionInput struct {
	SourceSessionID         uuid.UUID
	ExpectedSourceUpdatedAt time.Time
	SessionID               uuid.UUID
	Title                   string
	Metadata                map[string]any
	ProviderMessageID       string
	Role                    string
	Parts                   json.RawMessage
}

// ForkChatSession atomically verifies that the source is idle, creates a new
// independent aichat root, and persists its turnless transcript seed.
func (db *DB) ForkChatSession(ctx context.Context, input ForkChatSessionInput) (*Session, error) {
	if input.SourceSessionID == uuid.Nil || input.SessionID == uuid.Nil || input.SourceSessionID == input.SessionID {
		return nil, fmt.Errorf("%w: distinct source and fork session IDs are required", ErrInvalidSession)
	}
	var fork *Session
	err := db.Transaction(ctx, func(tx *DB) error {
		source, err := tx.LockSessionForUpdate(ctx, input.SourceSessionID)
		if err != nil {
			return err
		}
		if source.Source != "aichat" {
			return fmt.Errorf("%w: fork source %s has source %q", ErrSessionConflict, source.ID, source.Source)
		}
		if !input.ExpectedSourceUpdatedAt.IsZero() && !source.UpdatedAt.Equal(input.ExpectedSourceUpdatedAt) {
			return fmt.Errorf("%w: fork source %s changed after its transcript was read", ErrSessionConflict, source.ID)
		}
		var open turnRecord
		err = tx.gorm.WithContext(ctx).
			Where("session_id = ? AND status = ?", source.ID, TurnStatusOpen).
			First(&open).Error
		if err == nil {
			return fmt.Errorf("%w: session %s has active turn %s", ErrOpenChatTurn, source.ID, open.ID)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("read fork source turn: %w", err)
		}
		var createErr error
		fork, createErr = tx.CreateOrGetSession(ctx, CreateSessionInput{
			ID: input.SessionID, Source: "aichat", Provider: "captain", HostID: "local",
			Title: strings.TrimSpace(input.Title), Metadata: input.Metadata,
		})
		if createErr != nil {
			return createErr
		}
		return tx.PutChatMessage(ctx, PutChatMessageInput{
			SessionID: fork.ID, ProviderMessageID: input.ProviderMessageID,
			Role: input.Role, Parts: input.Parts,
		})
	})
	return fork, err
}

func (db *DB) DeleteChatSession(ctx context.Context, id uuid.UUID) error {
	if id == uuid.Nil {
		return fmt.Errorf("%w: session ID is required", ErrInvalidSession)
	}
	result := db.gorm.WithContext(ctx).Where("id = ? AND source = 'aichat'", id).Delete(&sessionRecord{})
	if result.Error != nil {
		return fmt.Errorf("delete Captain chat session: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	return nil
}

func (db *DB) touchChatSession(ctx context.Context, id uuid.UUID) error {
	result := db.gorm.WithContext(ctx).Model(&sessionRecord{}).Where("id = ?", id).
		Updates(map[string]any{
			"last_activity_at": gorm.Expr("GREATEST(last_activity_at, ?)", time.Now().UTC()),
		})
	if result.Error != nil {
		return fmt.Errorf("touch Captain chat session: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	return nil
}

func chatTurnFromRecord(record turnRecord) *ChatTurn {
	return &ChatTurn{
		ID: record.ID, SessionID: record.SessionID, ProviderTurnID: optionalString(record.ProviderTurnID),
		Index: record.TurnIndex, Status: record.Status,
	}
}
