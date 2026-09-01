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
	ID               uuid.UUID                 `json:"id"`
	SessionID        uuid.UUID                 `json:"sessionId"`
	PromptRunID      uuid.UUID                 `json:"promptRunId"`
	Provider         string                    `json:"provider"`
	Mode             api.RuntimeMode           `json:"mode"`
	SecretHash       []byte                    `json:"-"`
	Policy           map[string]api.ToolPolicy `json:"policy"`
	ExpiresAt        *time.Time                `json:"expiresAt,omitempty"`
	RevokedAt        *time.Time                `json:"revokedAt,omitempty"`
	RevocationReason string                    `json:"revocationReason,omitempty"`
	CreatedAt        time.Time                 `json:"createdAt"`
}

type CreateCallerToolCredentialInput struct {
	SessionID   uuid.UUID
	PromptRunID uuid.UUID
	Provider    string
	Mode        api.RuntimeMode
	SecretHash  []byte
	Policy      map[string]api.ToolPolicy
	ExpiresAt   *time.Time
}

type callerToolCredentialRecord struct {
	ID               uuid.UUID                 `gorm:"column:id;type:uuid;primaryKey"`
	SessionID        uuid.UUID                 `gorm:"column:session_id;type:uuid"`
	PromptRunID      uuid.UUID                 `gorm:"column:prompt_run_id;type:uuid"`
	Provider         string                    `gorm:"column:provider"`
	Mode             api.RuntimeMode           `gorm:"column:mode"`
	SecretHash       []byte                    `gorm:"column:secret_hash"`
	Policy           map[string]api.ToolPolicy `gorm:"column:policy;serializer:json;type:jsonb"`
	ExpiresAt        *time.Time                `gorm:"column:expires_at"`
	RevokedAt        *time.Time                `gorm:"column:revoked_at"`
	RevocationReason *string                   `gorm:"column:revocation_reason"`
	CreatedAt        time.Time                 `gorm:"column:created_at"`
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
		Provider: input.Provider, Mode: input.Mode,
		SecretHash: append([]byte(nil), input.SecretHash...),
		Policy:     cloneToolPolicy(input.Policy), ExpiresAt: input.ExpiresAt,
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
	if !(api.Runtime{Provider: input.Provider, Mode: input.Mode}).Valid() {
		return fmt.Errorf("%w: runtime %s/%s is invalid", ErrCallerToolCredentialInvalid, input.Provider, input.Mode)
	}
	if len(input.SecretHash) != 32 {
		return fmt.Errorf("%w: secret hash must be 32 bytes", ErrCallerToolCredentialInvalid)
	}
	if len(input.Policy) == 0 {
		return fmt.Errorf("%w: resolved policy is required", ErrCallerToolCredentialInvalid)
	}
	for tool, policy := range input.Policy {
		if strings.TrimSpace(tool) == "" || (policy != api.ToolPolicyAllow && policy != api.ToolPolicyAsk) {
			return fmt.Errorf("%w: tool %q has unresolved policy %q", ErrCallerToolCredentialInvalid, tool, policy)
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
	if input.SessionID == uuid.Nil || input.TurnID == uuid.Nil || input.PromptRunID == uuid.Nil || input.ModelCallID == uuid.Nil ||
		input.ToolCallID == "" || input.Tool == "" || !input.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("%w: session, turn, prompt run, model call, tool call, tool, and future expiry are required", ErrTurnRequestInvalid)
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
		ID: uuid.New(), SessionID: input.SessionID, TurnID: &input.TurnID, PromptRunID: &input.PromptRunID,
		ModelCallID: &input.ModelCallID, CredentialID: credentialID, ToolCallID: &input.ToolCallID,
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

func callerToolCredentialFromRecord(record callerToolCredentialRecord) CallerToolCredential {
	return CallerToolCredential{
		ID: record.ID, SessionID: record.SessionID, PromptRunID: record.PromptRunID,
		Provider: record.Provider, Mode: record.Mode,
		SecretHash: append([]byte(nil), record.SecretHash...),
		Policy:     cloneToolPolicy(record.Policy), ExpiresAt: record.ExpiresAt,
		RevokedAt: record.RevokedAt, RevocationReason: optionalString(record.RevocationReason),
		CreatedAt: record.CreatedAt,
	}
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

// cloneToolPolicy copies the policy map, normalizing the legacy spelling rows
// written before the tool vocabulary was unified.
//
// The policy column is untyped jsonb with no check constraint, so rows persisted
// by the old validator still hold "on" — a value api.ToolPolicy no longer
// recognises. It is read back with LegacyOn: allow because that is what "on"
// meant here: this map has no separate allow list, and the old validator
// accepted only "on" or "ask", so an "on" row is a tool that was cleared to run
// unprompted. An unrecognised value is left verbatim rather than defaulted, so
// it fails the caller's own check instead of silently becoming an authority
// nobody granted.
func cloneToolPolicy(policy map[string]api.ToolPolicy) map[string]api.ToolPolicy {
	cloned := make(map[string]api.ToolPolicy, len(policy))
	for tool, stored := range policy {
		if normalized, ok := api.ParseToolPolicy(string(stored), api.ParseToolPolicyOptions{
			LegacyOn: api.ToolPolicyAllow,
		}); ok {
			cloned[tool] = normalized
			continue
		}
		cloned[tool] = stored
	}
	return cloned
}
