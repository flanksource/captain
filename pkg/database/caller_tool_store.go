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
	ErrCallerToolCredentialInvalid  = errors.New("invalid caller-tool credential")
	ErrCallerToolCredentialNotFound = errors.New("caller-tool credential not found")
	ErrCallerToolCredentialInactive = errors.New("caller-tool credential is inactive")
	ErrTurnRequestInvalid           = errors.New("invalid Captain turn request")
	ErrTurnRequestNotFound          = errors.New("captain turn request not found")
	ErrTurnRequestConflict          = errors.New("captain turn request conflict")
)

type CallerToolCredential struct {
	ID               uuid.UUID               `json:"id"`
	SessionID        uuid.UUID               `json:"sessionId"`
	PromptRunID      uuid.UUID               `json:"promptRunId"`
	Backend          api.Backend             `json:"backend"`
	SecretHash       []byte                  `json:"-"`
	Policy           map[string]api.ToolMode `json:"policy"`
	ExpiresAt        *time.Time              `json:"expiresAt,omitempty"`
	RevokedAt        *time.Time              `json:"revokedAt,omitempty"`
	RevocationReason string                  `json:"revocationReason,omitempty"`
	CreatedAt        time.Time               `json:"createdAt"`
}

type CreateCallerToolCredentialInput struct {
	SessionID   uuid.UUID
	PromptRunID uuid.UUID
	Backend     api.Backend
	SecretHash  []byte
	Policy      map[string]api.ToolMode
	ExpiresAt   *time.Time
}

type callerToolCredentialRecord struct {
	ID               uuid.UUID               `gorm:"column:id;type:uuid;primaryKey"`
	SessionID        uuid.UUID               `gorm:"column:session_id;type:uuid"`
	PromptRunID      uuid.UUID               `gorm:"column:prompt_run_id;type:uuid"`
	Backend          api.Backend             `gorm:"column:backend"`
	SecretHash       []byte                  `gorm:"column:secret_hash"`
	Policy           map[string]api.ToolMode `gorm:"column:policy;serializer:json;type:jsonb"`
	ExpiresAt        *time.Time              `gorm:"column:expires_at"`
	RevokedAt        *time.Time              `gorm:"column:revoked_at"`
	RevocationReason *string                 `gorm:"column:revocation_reason"`
	CreatedAt        time.Time               `gorm:"column:created_at"`
}

func (callerToolCredentialRecord) TableName() string {
	return "captain_session_mcp_credentials"
}

func (db *DB) CreateCallerToolCredential(
	ctx context.Context,
	input CreateCallerToolCredentialInput,
) (*CallerToolCredential, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if err := validateCallerToolCredentialInput(input); err != nil {
		return nil, err
	}
	run, err := db.GetPromptRun(ctx, input.PromptRunID)
	if err != nil {
		return nil, err
	}
	if run.SessionID != input.SessionID {
		return nil, fmt.Errorf("%w: prompt run belongs to session %s", ErrCallerToolCredentialInvalid, run.SessionID)
	}
	record := callerToolCredentialRecord{
		ID: uuid.New(), SessionID: input.SessionID, PromptRunID: input.PromptRunID,
		Backend: input.Backend, SecretHash: append([]byte(nil), input.SecretHash...),
		Policy: cloneToolPolicy(input.Policy), ExpiresAt: input.ExpiresAt,
	}
	if err := db.gorm.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, fmt.Errorf("create caller-tool credential: %w", err)
	}
	return db.GetCallerToolCredential(ctx, record.ID)
}

func validateCallerToolCredentialInput(input CreateCallerToolCredentialInput) error {
	if input.SessionID == uuid.Nil || input.PromptRunID == uuid.Nil {
		return fmt.Errorf("%w: session and prompt run IDs are required", ErrCallerToolCredentialInvalid)
	}
	if !input.Backend.Valid() {
		return fmt.Errorf("%w: backend %q is invalid", ErrCallerToolCredentialInvalid, input.Backend)
	}
	if len(input.SecretHash) != 32 {
		return fmt.Errorf("%w: secret hash must be 32 bytes", ErrCallerToolCredentialInvalid)
	}
	if len(input.Policy) == 0 {
		return fmt.Errorf("%w: resolved policy is required", ErrCallerToolCredentialInvalid)
	}
	for tool, mode := range input.Policy {
		if strings.TrimSpace(tool) == "" || (mode != api.ToolModeOn && mode != api.ToolModeAsk) {
			return fmt.Errorf("%w: tool %q has unresolved mode %q", ErrCallerToolCredentialInvalid, tool, mode)
		}
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("%w: expiry must be in the future", ErrCallerToolCredentialInvalid)
	}
	return nil
}

func (db *DB) GetCallerToolCredential(ctx context.Context, id uuid.UUID) (*CallerToolCredential, error) {
	var record callerToolCredentialRecord
	if err := db.gorm.WithContext(ctx).First(&record, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrCallerToolCredentialNotFound, id)
		}
		return nil, fmt.Errorf("get caller-tool credential: %w", err)
	}
	credential := callerToolCredentialFromRecord(record)
	return &credential, nil
}

func (db *DB) ValidateCallerToolCredential(ctx context.Context, id uuid.UUID) error {
	credential, err := db.GetCallerToolCredential(ctx, id)
	if err != nil {
		return err
	}
	if credential.RevokedAt != nil ||
		(credential.ExpiresAt != nil && !time.Now().Before(*credential.ExpiresAt)) {
		return fmt.Errorf("%w: %s", ErrCallerToolCredentialInactive, id)
	}
	return nil
}

func (db *DB) RevokeCallerToolCredential(ctx context.Context, id uuid.UUID, reason string) error {
	now := time.Now().UTC()
	result := db.gorm.WithContext(ctx).Model(&callerToolCredentialRecord{}).
		Where("id = ? AND revoked_at IS NULL", id).
		Updates(map[string]any{"revoked_at": now, "revocation_reason": nullableTrimmed(reason)})
	if result.Error != nil {
		return fmt.Errorf("revoke caller-tool credential: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}
	credential, err := db.GetCallerToolCredential(ctx, id)
	if err != nil {
		return err
	}
	if credential.RevokedAt != nil {
		return nil
	}
	return fmt.Errorf("%w: credential %s was not revoked", ErrCallerToolCredentialInactive, id)
}

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
	PromptRunID    *uuid.UUID       `json:"promptRunId,omitempty"`
	CredentialID   *uuid.UUID       `json:"credentialId,omitempty"`
	ToolCallID     string           `json:"toolCallId,omitempty"`
	Kind           string           `json:"kind"`
	State          TurnRequestState `json:"state"`
	Request        map[string]any   `json:"request"`
	Response       map[string]any   `json:"response,omitempty"`
	IdempotencyKey string           `json:"idempotencyKey,omitempty"`
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
	PromptRunID    *uuid.UUID       `gorm:"column:prompt_run_id;type:uuid"`
	CredentialID   *uuid.UUID       `gorm:"column:credential_id;type:uuid"`
	ToolCallID     *string          `gorm:"column:tool_call_id"`
	Kind           string           `gorm:"column:kind"`
	State          TurnRequestState `gorm:"column:state"`
	Request        map[string]any   `gorm:"column:request;serializer:json;type:jsonb"`
	Response       map[string]any   `gorm:"column:response;serializer:json;type:jsonb"`
	IdempotencyKey *string          `gorm:"column:idempotency_key"`
	ResolvedBy     *string          `gorm:"column:resolved_by"`
	Reason         *string          `gorm:"column:reason"`
	Version        int64            `gorm:"column:version"`
	ExpiresAt      *time.Time       `gorm:"column:expires_at"`
	CreatedAt      time.Time        `gorm:"column:created_at"`
	ResolvedAt     *time.Time       `gorm:"column:resolved_at"`
}

func (turnRequestRecord) TableName() string { return "captain_turn_requests" }

type CreateToolApprovalRequestInput struct {
	CredentialID uuid.UUID
	SessionID    uuid.UUID
	PromptRunID  uuid.UUID
	ToolCallID   string
	Tool         string
	Input        map[string]any
	ExpiresAt    time.Time
}

func (db *DB) CreateToolApprovalRequest(
	ctx context.Context,
	input CreateToolApprovalRequestInput,
) (*TurnRequest, error) {
	if err := db.ValidateCallerToolCredential(ctx, input.CredentialID); err != nil {
		return nil, err
	}
	credential, err := db.GetCallerToolCredential(ctx, input.CredentialID)
	if err != nil {
		return nil, err
	}
	if credential.SessionID != input.SessionID || credential.PromptRunID != input.PromptRunID {
		return nil, fmt.Errorf("%w: credential does not belong to the supplied session and run", ErrTurnRequestInvalid)
	}
	input.ToolCallID = strings.TrimSpace(input.ToolCallID)
	input.Tool = strings.TrimSpace(input.Tool)
	if input.ToolCallID == "" || input.Tool == "" || !input.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("%w: tool call ID, tool, and future expiry are required", ErrTurnRequestInvalid)
	}
	if credential.Policy[input.Tool] != api.ToolModeAsk {
		return nil, fmt.Errorf("%w: tool %q is not approved by ask policy", ErrTurnRequestInvalid, input.Tool)
	}
	if credential.ExpiresAt != nil && input.ExpiresAt.After(*credential.ExpiresAt) {
		input.ExpiresAt = *credential.ExpiresAt
	}
	idempotencyKey := "mcp:" + input.CredentialID.String() + ":" + input.ToolCallID
	request := map[string]any{
		"credentialId": input.CredentialID.String(), "tool": input.Tool, "input": input.Input,
	}
	record := turnRequestRecord{
		ID: uuid.New(), SessionID: input.SessionID, PromptRunID: &input.PromptRunID,
		CredentialID: &input.CredentialID, ToolCallID: &input.ToolCallID,
		Kind: "tool_approval", State: TurnRequestStatePending, Request: request,
		IdempotencyKey: &idempotencyKey, ExpiresAt: &input.ExpiresAt,
	}
	result := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return nil, fmt.Errorf("create tool approval request: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return db.GetTurnRequest(ctx, record.ID)
	}
	var existing turnRequestRecord
	if err := db.gorm.WithContext(ctx).
		Where("credential_id = ? AND tool_call_id = ?", input.CredentialID, input.ToolCallID).
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
	SessionID    uuid.UUID
	ToolCallID   string
	Approved     bool
	UpdatedInput map[string]any
	ResolvedBy   string
	Reason       string
}

func (db *DB) ResolveToolApprovalRequest(
	ctx context.Context,
	input ResolveToolApprovalRequestInput,
) (*TurnRequest, error) {
	if input.SessionID == uuid.Nil || strings.TrimSpace(input.ToolCallID) == "" {
		return nil, fmt.Errorf("%w: session and tool call IDs are required", ErrTurnRequestInvalid)
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
		Where("session_id = ? AND tool_call_id = ? AND kind = 'tool_approval' AND state = 'pending'",
			input.SessionID, strings.TrimSpace(input.ToolCallID)).
		Order("created_at DESC").First(&pending).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: session %s tool call %s", ErrTurnRequestNotFound, input.SessionID, input.ToolCallID)
		}
		return nil, fmt.Errorf("read pending tool approval request: %w", err)
	}
	now := time.Now().UTC()
	result := db.gorm.WithContext(ctx).Model(&turnRequestRecord{}).
		Where("id = ? AND state = 'pending'", pending.ID).
		Where(`EXISTS (
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
		if pending.CredentialID != nil {
			if err := db.ValidateCallerToolCredential(ctx, *pending.CredentialID); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("%w: session %s tool call %s", ErrTurnRequestNotFound, input.SessionID, input.ToolCallID)
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

func callerToolCredentialFromRecord(record callerToolCredentialRecord) CallerToolCredential {
	return CallerToolCredential{
		ID: record.ID, SessionID: record.SessionID, PromptRunID: record.PromptRunID,
		Backend: record.Backend, SecretHash: append([]byte(nil), record.SecretHash...),
		Policy: cloneToolPolicy(record.Policy), ExpiresAt: record.ExpiresAt,
		RevokedAt: record.RevokedAt, RevocationReason: optionalString(record.RevocationReason),
		CreatedAt: record.CreatedAt,
	}
}

func turnRequestFromRecord(record turnRequestRecord) TurnRequest {
	return TurnRequest{
		ID: record.ID, SessionID: record.SessionID, PromptRunID: record.PromptRunID,
		CredentialID: record.CredentialID, ToolCallID: optionalString(record.ToolCallID),
		Kind: record.Kind, State: record.State, Request: record.Request, Response: record.Response,
		IdempotencyKey: optionalString(record.IdempotencyKey), ResolvedBy: optionalString(record.ResolvedBy),
		Reason: optionalString(record.Reason), Version: record.Version, ExpiresAt: record.ExpiresAt,
		CreatedAt: record.CreatedAt, ResolvedAt: record.ResolvedAt,
	}
}

func cloneToolPolicy(policy map[string]api.ToolMode) map[string]api.ToolMode {
	cloned := make(map[string]api.ToolMode, len(policy))
	for tool, mode := range policy {
		cloned[tool] = mode
	}
	return cloned
}
