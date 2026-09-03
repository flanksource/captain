package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrCallerToolCredentialInvalid  = errors.New("invalid caller-tool credential")
	ErrCallerToolCredentialNotFound = errors.New("caller-tool credential not found")
	ErrCallerToolCredentialInactive = errors.New("caller-tool credential is inactive")
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
