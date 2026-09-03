package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrTurnRequestInvalid  = errors.New("invalid Captain turn request")
	ErrTurnRequestNotFound = errors.New("captain turn request not found")
	ErrTurnRequestConflict = errors.New("captain turn request conflict")
)

type TurnRequestState string

const (
	TurnRequestStatePending   TurnRequestState = "pending"
	TurnRequestStateApproved  TurnRequestState = "approved"
	TurnRequestStateDenied    TurnRequestState = "denied"
	TurnRequestStateCancelled TurnRequestState = "cancelled"
	TurnRequestStateExpired   TurnRequestState = "expired"
)

type TurnRequest struct {
	ID             uuid.UUID        `json:"id"`
	SessionID      uuid.UUID        `json:"sessionId"`
	TurnID         *uuid.UUID       `json:"turnId,omitempty"`
	PromptRunID    *uuid.UUID       `json:"promptRunId,omitempty"`
	ModelCallID    *uuid.UUID       `json:"modelCallId,omitempty"`
	CredentialID   *uuid.UUID       `json:"-"`
	ToolCallID     string           `json:"toolCallId,omitempty"`
	Kind           string           `json:"kind"`
	State          TurnRequestState `json:"state"`
	Request        map[string]any   `json:"request"`
	Response       map[string]any   `json:"response,omitempty"`
	IdempotencyKey string           `json:"idempotencyKey,omitempty"`
	RequestedBy    string           `json:"requestedBy,omitempty"`
	ResolvedBy     string           `json:"resolvedBy,omitempty"`
	Reason         string           `json:"reason,omitempty"`
	Version        int64            `json:"version"`
	ExpiresAt      *time.Time       `json:"expiresAt,omitempty"`
	CreatedAt      time.Time        `json:"createdAt"`
	ResolvedAt     *time.Time       `json:"resolvedAt,omitempty"`
}

type turnRequestRecord struct {
	ID             uuid.UUID        `gorm:"column:id;type:uuid;primaryKey"`
	SessionID      uuid.UUID        `gorm:"column:session_id;type:uuid"`
	TurnID         *uuid.UUID       `gorm:"column:turn_id;type:uuid"`
	PromptRunID    *uuid.UUID       `gorm:"column:prompt_run_id;type:uuid"`
	ModelCallID    *uuid.UUID       `gorm:"column:model_call_id;type:uuid"`
	CredentialID   *uuid.UUID       `gorm:"column:credential_id;type:uuid"`
	ToolCallID     *string          `gorm:"column:tool_call_id"`
	Kind           string           `gorm:"column:kind"`
	State          TurnRequestState `gorm:"column:state"`
	Request        map[string]any   `gorm:"column:request;serializer:json;type:jsonb"`
	Response       map[string]any   `gorm:"column:response;serializer:json;type:jsonb"`
	IdempotencyKey *string          `gorm:"column:idempotency_key"`
	RequestedBy    *string          `gorm:"column:requested_by"`
	ResolvedBy     *string          `gorm:"column:resolved_by"`
	Reason         *string          `gorm:"column:reason"`
	Version        int64            `gorm:"column:version"`
	ExpiresAt      *time.Time       `gorm:"column:expires_at"`
	CreatedAt      time.Time        `gorm:"column:created_at"`
	ResolvedAt     *time.Time       `gorm:"column:resolved_at"`
}

func (turnRequestRecord) TableName() string { return "captain_turn_requests" }

// CreateToolApprovalRequestInput describes one durable tool approval. TurnID and
// ModelCallID are required on the caller-tool path (CredentialID set) and
// optional on the credential-less provider path, where a streaming provider or
// an external host has a session and a prompt run but never opens a turn or a
// model call. uuid.Nil writes NULL.
type CreateToolApprovalRequestInput struct {
	CredentialID uuid.UUID
	SessionID    uuid.UUID
	TurnID       uuid.UUID
	PromptRunID  uuid.UUID
	ModelCallID  uuid.UUID
	ToolCallID   string
	Tool         string
	Input        map[string]any
	RequestedBy  string
	ExpiresAt    time.Time
}

func (db *DB) CreateToolApprovalRequest(
	ctx context.Context,
	input CreateToolApprovalRequestInput,
) (*TurnRequest, error) {
	var credential *CallerToolCredential
	if input.CredentialID != uuid.Nil {
		if err := db.ValidateCallerToolCredential(ctx, input.CredentialID); err != nil {
			return nil, err
		}
		var err error
		credential, err = db.GetCallerToolCredential(ctx, input.CredentialID)
		if err != nil {
			return nil, err
		}
		if credential.SessionID != input.SessionID || credential.PromptRunID != input.PromptRunID {
			return nil, fmt.Errorf("%w: credential does not belong to the supplied session and run", ErrTurnRequestInvalid)
		}
	}
	input.ToolCallID = strings.TrimSpace(input.ToolCallID)
	input.Tool = strings.TrimSpace(input.Tool)
	if input.SessionID == uuid.Nil || input.PromptRunID == uuid.Nil ||
		input.ToolCallID == "" || input.Tool == "" || !input.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("%w: session, prompt run, tool call, tool, and future expiry are required", ErrTurnRequestInvalid)
	}
	if credential != nil && (input.TurnID == uuid.Nil || input.ModelCallID == uuid.Nil) {
		return nil, fmt.Errorf("%w: a caller-tool approval requires its turn and model call", ErrTurnRequestInvalid)
	}
	if credential != nil && credential.Policy[input.Tool] != api.ToolPolicyAsk {
		return nil, fmt.Errorf("%w: tool %q is not approved by ask policy", ErrTurnRequestInvalid, input.Tool)
	}
	if credential != nil && credential.ExpiresAt != nil && input.ExpiresAt.After(*credential.ExpiresAt) {
		input.ExpiresAt = *credential.ExpiresAt
	}
	idempotencyKey := "provider:" + input.PromptRunID.String() + ":" + input.ToolCallID
	if credential != nil {
		idempotencyKey = "mcp:" + input.CredentialID.String() + ":" + input.ToolCallID
	}
	request := map[string]any{
		"tool": input.Tool, "input": input.Input,
	}
	var credentialID *uuid.UUID
	if credential != nil {
		credentialID = &input.CredentialID
	}
	record := turnRequestRecord{
		ID: uuid.New(), SessionID: input.SessionID, TurnID: nullableUUID(input.TurnID), PromptRunID: &input.PromptRunID,
		ModelCallID: nullableUUID(input.ModelCallID), CredentialID: credentialID, ToolCallID: &input.ToolCallID,
		Kind: "tool_approval", State: TurnRequestStatePending, Request: request,
		IdempotencyKey: &idempotencyKey, RequestedBy: nullableTrimmed(input.RequestedBy), ExpiresAt: &input.ExpiresAt,
	}
	result := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return nil, fmt.Errorf("create tool approval request: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		if err := db.touchChatSession(ctx, input.SessionID); err != nil {
			return nil, err
		}
		return db.GetTurnRequest(ctx, record.ID)
	}
	var existing turnRequestRecord
	if err := db.gorm.WithContext(ctx).
		Where("session_id = ? AND idempotency_key = ?", input.SessionID, idempotencyKey).
		First(&existing).Error; err != nil {
		return nil, fmt.Errorf("read existing tool approval request: %w", err)
	}
	if !reflect.DeepEqual(existing.Request, request) {
		return nil, fmt.Errorf("%w: tool call %q was retried with different input", ErrTurnRequestConflict, input.ToolCallID)
	}
	out := turnRequestFromRecord(existing)
	return &out, nil
}

type ResolveToolApprovalRequestInput struct {
	SessionID      uuid.UUID
	RequestID      uuid.UUID
	ExpectedTurnID *uuid.UUID
	Approved       bool
	UpdatedInput   map[string]any
	ResolvedBy     string
	Reason         string
}

func (db *DB) ResolveToolApprovalRequest(
	ctx context.Context,
	input ResolveToolApprovalRequestInput,
) (*TurnRequest, error) {
	if input.SessionID == uuid.Nil || input.RequestID == uuid.Nil {
		return nil, fmt.Errorf("%w: session and approval request IDs are required", ErrTurnRequestInvalid)
	}
	state := TurnRequestStateDenied
	var response map[string]any
	if input.Approved {
		state = TurnRequestStateApproved
		if input.UpdatedInput != nil {
			response = map[string]any{"updatedInput": input.UpdatedInput}
		}
	}
	var pending turnRequestRecord
	if err := db.gorm.WithContext(ctx).
		Where("id = ? AND session_id = ? AND kind = 'tool_approval'", input.RequestID, input.SessionID).
		First(&pending).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: session %s approval %s", ErrTurnRequestNotFound, input.SessionID, input.RequestID)
		}
		return nil, fmt.Errorf("read tool approval request: %w", err)
	}
	if pending.State != TurnRequestStatePending {
		if pending.State == state && reflect.DeepEqual(pending.Response, response) &&
			optionalString(pending.Reason) == strings.TrimSpace(input.Reason) {
			out := turnRequestFromRecord(pending)
			return &out, nil
		}
		return nil, fmt.Errorf("%w: approval %s already has a different %s decision", ErrTurnRequestConflict, pending.ID, pending.State)
	}
	if input.ExpectedTurnID != nil && (pending.TurnID == nil || *pending.TurnID != *input.ExpectedTurnID) {
		return nil, fmt.Errorf("%w: approval %s does not belong to active turn %s", ErrTurnRequestConflict, pending.ID, *input.ExpectedTurnID)
	}
	now := time.Now().UTC()
	result := db.gorm.WithContext(ctx).Model(&turnRequestRecord{}).
		Where("id = ? AND state = 'pending'", pending.ID).
		Where(`credential_id IS NOT NULL OR EXISTS (
			SELECT 1 FROM captain_prompt_runs run
			WHERE run.id = captain_turn_requests.prompt_run_id AND run.state = 'waiting'
		)`).
		Where(`credential_id IS NULL OR EXISTS (
			SELECT 1 FROM captain_session_mcp_credentials credential
			WHERE credential.id = captain_turn_requests.credential_id
			  AND credential.revoked_at IS NULL
			  AND (credential.expires_at IS NULL OR credential.expires_at > ?)
		)`, now).
		Updates(map[string]any{
			"state": state, "response": response, "resolved_by": nullableTrimmed(input.ResolvedBy),
			"reason": nullableTrimmed(input.Reason), "resolved_at": now,
		})
	if result.Error != nil {
		return nil, fmt.Errorf("resolve tool approval request: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		if pending.CredentialID == nil {
			return nil, fmt.Errorf("%w: approval %s cannot be resolved before its prompt run is waiting", ErrTurnRequestConflict, pending.ID)
		}
		if pending.CredentialID != nil {
			if err := db.ValidateCallerToolCredential(ctx, *pending.CredentialID); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("%w: session %s approval %s", ErrTurnRequestNotFound, input.SessionID, input.RequestID)
	}
	if err := db.touchChatSession(ctx, input.SessionID); err != nil {
		return nil, err
	}
	return db.GetTurnRequest(ctx, pending.ID)
}

func (db *DB) GetTurnRequest(ctx context.Context, id uuid.UUID) (*TurnRequest, error) {
	var record turnRequestRecord
	if err := db.gorm.WithContext(ctx).First(&record, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrTurnRequestNotFound, id)
		}
		return nil, fmt.Errorf("get Captain turn request: %w", err)
	}
	out := turnRequestFromRecord(record)
	return &out, nil
}

type TurnRequestFilter struct {
	SessionID   uuid.UUID
	PromptRunID *uuid.UUID
}

func (db *DB) ListTurnRequests(ctx context.Context, filter TurnRequestFilter) ([]TurnRequest, error) {
	if filter.SessionID == uuid.Nil {
		return nil, fmt.Errorf("%w: session ID is required", ErrTurnRequestInvalid)
	}
	query := db.gorm.WithContext(ctx).Where("session_id = ?", filter.SessionID).Order("created_at, id")
	if filter.PromptRunID != nil {
		query = query.Where("prompt_run_id = ?", *filter.PromptRunID)
	}
	var records []turnRequestRecord
	if err := query.Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list Captain turn requests: %w", err)
	}
	requests := make([]TurnRequest, len(records))
	for i := range records {
		requests[i] = turnRequestFromRecord(records[i])
	}
	return requests, nil
}

func (db *DB) ExpireToolApprovalRequest(ctx context.Context, id uuid.UUID, state TurnRequestState, reason string) error {
	if state != TurnRequestStateExpired && state != TurnRequestStateCancelled {
		return fmt.Errorf("%w: terminal state %q is invalid", ErrTurnRequestInvalid, state)
	}
	now := time.Now().UTC()
	result := db.gorm.WithContext(ctx).Model(&turnRequestRecord{}).
		Where("id = ? AND state = 'pending'", id).
		Updates(map[string]any{"state": state, "reason": nullableTrimmed(reason), "resolved_at": now})
	if result.Error != nil {
		return fmt.Errorf("expire tool approval request: %w", result.Error)
	}
	return nil
}

func (db *DB) CancelPendingTurnRequests(ctx context.Context, sessionID, promptRunID uuid.UUID, reason string) error {
	if sessionID == uuid.Nil || promptRunID == uuid.Nil {
		return fmt.Errorf("%w: session and prompt run IDs are required", ErrTurnRequestInvalid)
	}
	now := time.Now().UTC()
	result := db.gorm.WithContext(ctx).Model(&turnRequestRecord{}).
		Where("session_id = ? AND prompt_run_id = ? AND state = 'pending'", sessionID, promptRunID).
		Updates(map[string]any{
			"state": TurnRequestStateCancelled, "reason": nullableTrimmed(reason), "resolved_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("cancel pending Captain turn requests: %w", result.Error)
	}
	return nil
}

func nullableUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func turnRequestFromRecord(record turnRequestRecord) TurnRequest {
	return TurnRequest{
		ID: record.ID, SessionID: record.SessionID, TurnID: record.TurnID, PromptRunID: record.PromptRunID, ModelCallID: record.ModelCallID,
		CredentialID: record.CredentialID, ToolCallID: optionalString(record.ToolCallID),
		Kind: record.Kind, State: record.State, Request: record.Request, Response: record.Response,
		IdempotencyKey: optionalString(record.IdempotencyKey), RequestedBy: optionalString(record.RequestedBy), ResolvedBy: optionalString(record.ResolvedBy),
		Reason: optionalString(record.Reason), Version: record.Version, ExpiresAt: record.ExpiresAt,
		CreatedAt: record.CreatedAt, ResolvedAt: record.ResolvedAt,
	}
}
