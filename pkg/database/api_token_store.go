// Storage for the bearer credentials that reach this captain over the network.
//
// A token here is durable rather than single-use: it stays valid until it
// expires or is revoked, which is what lets a restarting or rescheduled sidecar
// re-present the same credential instead of crash-looping on a spent one. Only
// the argon2id hash is stored, and it never leaves this package except through
// LookupAPIToken, whose result feeds captaintoken's constant-time verifier.

package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/captaintoken"
	"github.com/flanksource/clicky/text"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrAPITokenInvalid  = errors.New("invalid captain API token")
	ErrAPITokenNotFound = errors.New("captain API token not found")
	// ErrAPITokenPoolFull means the credential is good but its pool has no slot
	// left. It is separated from a rejection so an operator sees a capacity
	// problem rather than hunting a phantom auth failure.
	ErrAPITokenPoolFull = errors.New("captain API token pool is full")
)

const (
	// maxPoolTokenNameLen leaves room for the "-999" a derived member name adds
	// without pushing the result past the 64-character ref-segment bound.
	maxPoolTokenNameLen = 60
	// maxPoolAgents bounds the derived ordinals, matching that suffix width.
	maxPoolAgents = 999
	// touchInterval throttles last_used_at. Git smart-HTTP makes several
	// requests per push, so writing on each one would amplify a single push into
	// a burst of row updates that carry no extra information.
	touchInterval = time.Minute
)

type apiTokenRecord struct {
	ID               uuid.UUID          `gorm:"column:id;type:uuid;primaryKey"`
	TokenID          string             `gorm:"column:token_id"`
	SecretHash       string             `gorm:"column:secret_hash"`
	Name             string             `gorm:"column:name"`
	Scope            captaintoken.Scope `gorm:"column:scope"`
	Agent            *string            `gorm:"column:agent"`
	Pool             bool               `gorm:"column:pool"`
	PoolAgents       []string           `gorm:"column:pool_agents;serializer:json;type:jsonb"`
	MaxAgents        *int               `gorm:"column:max_agents"`
	ExpiresAt        *time.Time         `gorm:"column:expires_at"`
	RevokedAt        *time.Time         `gorm:"column:revoked_at"`
	RevocationReason *string            `gorm:"column:revocation_reason"`
	LastUsedAt       *time.Time         `gorm:"column:last_used_at"`
	CreatedAt        time.Time          `gorm:"column:created_at"`
}

func (apiTokenRecord) TableName() string { return "captain_api_tokens" }

// APIToken is a token as listings and the CLI see it. It deliberately has no
// hash field: the stored secret cannot leak through a struct that never carries
// it, which is a stronger guarantee than a `json:"-"` tag someone can drop.
type APIToken struct {
	ID               uuid.UUID          `json:"id"`
	TokenID          string             `json:"tokenId"`
	Name             string             `json:"name"`
	Scope            captaintoken.Scope `json:"scope"`
	Agent            string             `json:"agent,omitempty"`
	Pool             bool               `json:"pool"`
	PoolAgents       []string           `json:"poolAgents,omitempty"`
	MaxAgents        int                `json:"maxAgents,omitempty"`
	ExpiresAt        *time.Time         `json:"expiresAt,omitempty"`
	RevokedAt        *time.Time         `json:"revokedAt,omitempty"`
	RevocationReason string             `json:"revocationReason,omitempty"`
	LastUsedAt       *time.Time         `json:"lastUsedAt,omitempty"`
	CreatedAt        time.Time          `json:"createdAt"`
}

// CreateAPITokenInput describes the credential to mint. Pool and Agent are the
// two ways of answering "who is this?" and exactly one applies.
type CreateAPITokenInput struct {
	Name      string
	Scope     captaintoken.Scope
	Agent     string
	Pool      bool
	MaxAgents int
	ExpiresAt *time.Time
}

// CreateAPIToken mints a token and stores only its hash.
//
// The returned SensitiveString is the sole existence of the plaintext: nothing
// stored can reconstruct it, so a caller that discards it has to mint again.
func (db *DB) CreateAPIToken(
	ctx context.Context,
	input CreateAPITokenInput,
) (*APIToken, text.SensitiveString, error) {
	if err := db.requireGorm(); err != nil {
		return nil, "", err
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Agent = strings.TrimSpace(input.Agent)
	if err := validateAPITokenInput(input); err != nil {
		return nil, "", err
	}
	minted, err := captaintoken.Mint()
	if err != nil {
		return nil, "", err
	}
	record := apiTokenRecord{
		ID: uuid.New(), TokenID: minted.ID, SecretHash: minted.Hash,
		Name: input.Name, Scope: input.Scope, Agent: nullableTrimmed(input.Agent),
		Pool: input.Pool, PoolAgents: []string{}, ExpiresAt: input.ExpiresAt,
	}
	if input.MaxAgents > 0 {
		record.MaxAgents = &input.MaxAgents
	}
	if err := db.gorm.WithContext(ctx).Create(&record).Error; err != nil {
		return nil, "", fmt.Errorf("create captain API token: %w", err)
	}
	token := record.toAPIToken()
	return &token, minted.Secret, nil
}

func validateAPITokenInput(input CreateAPITokenInput) error {
	if err := captaintoken.ValidateName(input.Name); err != nil {
		return fmt.Errorf("%w: token %s", ErrAPITokenInvalid, err)
	}
	if !input.Scope.Valid() {
		return fmt.Errorf("%w: scope %q must be %s or %s",
			ErrAPITokenInvalid, input.Scope, captaintoken.ScopeGit, captaintoken.ScopeAPI)
	}
	if err := validateAPITokenIdentity(input); err != nil {
		return err
	}
	if input.ExpiresAt != nil && !input.ExpiresAt.After(time.Now()) {
		return fmt.Errorf("%w: expiry must be in the future", ErrAPITokenInvalid)
	}
	return nil
}

// validateAPITokenIdentity mirrors the captain_api_tokens_identity check so an
// impossible combination is named in prose rather than surfacing as a constraint
// violation an operator has to decode.
func validateAPITokenIdentity(input CreateAPITokenInput) error {
	if input.Scope == captaintoken.ScopeAPI {
		if input.Pool || input.Agent != "" {
			return fmt.Errorf("%w: an %s-scoped token is neither pooled nor bound to an agent",
				ErrAPITokenInvalid, captaintoken.ScopeAPI)
		}
		return nil
	}
	if !input.Pool {
		if input.MaxAgents != 0 {
			return fmt.Errorf("%w: max-agents applies to a pool token; this one is bound to a single agent", ErrAPITokenInvalid)
		}
		if err := captaintoken.ValidateName(input.Agent); err != nil {
			return fmt.Errorf("%w: a %s-scoped token must name the agent it belongs to, or be created as a pool: %s",
				ErrAPITokenInvalid, captaintoken.ScopeGit, err)
		}
		return nil
	}
	if input.Agent != "" {
		return fmt.Errorf("%w: a pool token names its members as they arrive; it cannot also be bound to agent %q",
			ErrAPITokenInvalid, input.Agent)
	}
	if len(input.Name) > maxPoolTokenNameLen {
		return fmt.Errorf("%w: a pool name is at most %d characters, so a derived member name still fits a ref segment",
			ErrAPITokenInvalid, maxPoolTokenNameLen)
	}
	if input.MaxAgents < 0 || input.MaxAgents > maxPoolAgents {
		return fmt.Errorf("%w: max-agents must be between 1 and %d, or 0 for unbounded", ErrAPITokenInvalid, maxPoolAgents)
	}
	return nil
}

// LookupAPIToken resolves a token by its public id. The signature matches
// captaintoken.Lookup so it can be handed to a verifier directly.
//
// An absent row is ErrUnknown, the same answer a wrong secret gets: telling a
// caller which half they got right is a probing aid.
func (db *DB) LookupAPIToken(ctx context.Context, tokenID string) (captaintoken.Record, error) {
	if err := db.requireGorm(); err != nil {
		return captaintoken.Record{}, err
	}
	var record apiTokenRecord
	if err := db.gorm.WithContext(ctx).First(&record, "token_id = ?", tokenID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return captaintoken.Record{}, captaintoken.ErrUnknown
		}
		return captaintoken.Record{}, fmt.Errorf("look up captain API token: %w", err)
	}
	return record.toVerifierRecord(), nil
}

// GetAPIToken reads one token for display. ok is false when there is no such id.
func (db *DB) GetAPIToken(ctx context.Context, tokenID string) (*APIToken, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var record apiTokenRecord
	if err := db.gorm.WithContext(ctx).First(&record, "token_id = ?", tokenID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrAPITokenNotFound, tokenID)
		}
		return nil, fmt.Errorf("get captain API token: %w", err)
	}
	token := record.toAPIToken()
	return &token, nil
}

// AdmitAPITokenAgent resolves a verified credential to the agent identity it
// speaks for, allocating a pool slot when it needs one.
//
// requestedName is what a returning member persisted from an earlier admission.
// It is honoured only when it is already on file: names are derived by the
// supervisor, so a client can neither invent an identity outside the pool's
// naming nor squat on a sibling's ref namespace. That is also what makes a
// restart free — a returning member reclaims its name instead of burning a slot.
func (db *DB) AdmitAPITokenAgent(ctx context.Context, tokenID, requestedName string) (string, error) {
	if err := db.requireGorm(); err != nil {
		return "", err
	}
	requestedName = strings.TrimSpace(requestedName)
	var admitted string
	err := db.gorm.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// FOR UPDATE, because two sidecars starting together would otherwise
		// each read the same member list and the second write would silently
		// erase the first — overrunning max_agents by exactly the race width.
		var record apiTokenRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&record, "token_id = ?", tokenID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return captaintoken.ErrUnknown
			}
			return fmt.Errorf("read captain API token: %w", err)
		}
		if err := record.toVerifierRecord().Active(time.Now()); err != nil {
			return err
		}
		name, err := admitAgainst(&record, requestedName)
		if err != nil {
			return err
		}
		admitted = name
		if slices.Contains(record.PoolAgents, name) {
			return nil
		}
		return persistPoolMember(tx, record, name)
	})
	if err != nil {
		return "", err
	}
	return admitted, nil
}

// admitAgainst decides which name a presented token speaks for, without writing.
func admitAgainst(record *apiTokenRecord, requestedName string) (string, error) {
	if !record.Pool {
		bound := derefString(record.Agent)
		if requestedName != "" && requestedName != bound {
			return "", fmt.Errorf("%w: token %q is bound to agent %q and cannot act as %q",
				ErrAPITokenInvalid, record.Name, bound, requestedName)
		}
		return bound, nil
	}
	if requestedName != "" && slices.Contains(record.PoolAgents, requestedName) {
		return requestedName, nil
	}
	if record.MaxAgents != nil && len(record.PoolAgents) >= *record.MaxAgents {
		return "", fmt.Errorf("%w: %q already has its %d members; revoke one or raise max-agents",
			ErrAPITokenPoolFull, record.Name, *record.MaxAgents)
	}
	return nextPoolAgentName(record.Name, record.PoolAgents)
}

// nextPoolAgentName derives the lowest unused member name under a pool.
func nextPoolAgentName(pool string, taken []string) (string, error) {
	for ordinal := 1; ordinal <= maxPoolAgents; ordinal++ {
		if candidate := fmt.Sprintf("%s-%02d", pool, ordinal); !slices.Contains(taken, candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: %q has exhausted its %d derivable member names", ErrAPITokenPoolFull, pool, maxPoolAgents)
}

// persistPoolMember appends a newly derived name. The value is cast explicitly
// because gorm applies a field serializer to a model write, not to the column
// update this needs.
func persistPoolMember(tx *gorm.DB, record apiTokenRecord, name string) error {
	encoded, err := json.Marshal(append(slices.Clone(record.PoolAgents), name))
	if err != nil {
		return fmt.Errorf("encode captain API token pool members: %w", err)
	}
	err = tx.Model(&apiTokenRecord{}).Where("id = ?", record.ID).
		Update("pool_agents", gorm.Expr("?::jsonb", string(encoded))).Error
	if err != nil {
		return fmt.Errorf("admit %q to captain API token pool %q: %w", name, record.Name, err)
	}
	return nil
}

// TouchAPIToken records that a token was used, at most once per touchInterval.
func (db *DB) TouchAPIToken(ctx context.Context, tokenID string) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	err := db.gorm.WithContext(ctx).Model(&apiTokenRecord{}).
		Where("token_id = ? AND (last_used_at IS NULL OR last_used_at < ?)", tokenID, time.Now().Add(-touchInterval)).
		Update("last_used_at", clause.Expr{SQL: "now()"}).Error
	if err != nil {
		return fmt.Errorf("record captain API token use: %w", err)
	}
	return nil
}

// ListAPITokensFilter narrows a listing. Revoked tokens are hidden by default:
// the list answers "what can reach this captain right now?".
type ListAPITokensFilter struct {
	Scope          captaintoken.Scope
	Agent          string
	IncludeRevoked bool
	Limit          int
}

// ListAPITokens returns tokens newest first, always as a slice so a caller can
// iterate it unconditionally.
func (db *DB) ListAPITokens(ctx context.Context, filter ListAPITokensFilter) ([]APIToken, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	query := db.gorm.WithContext(ctx).Model(&apiTokenRecord{})
	if filter.Scope != "" {
		query = query.Where("scope = ?", filter.Scope)
	}
	if agent := nullableTrimmed(filter.Agent); agent != nil {
		// A pool member is named in pool_agents, not in the bound agent column,
		// so a search by agent has to look in both or it silently misses pools.
		query = query.Where("agent = ? OR pool_agents @> ?::jsonb", *agent, `["`+*agent+`"]`)
	}
	if !filter.IncludeRevoked {
		query = query.Where("revoked_at IS NULL")
	}
	limit := filter.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var records []apiTokenRecord
	if err := query.Order("created_at DESC, id DESC").Limit(limit).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list captain API tokens: %w", err)
	}
	tokens := make([]APIToken, 0, len(records))
	for _, record := range records {
		tokens = append(tokens, record.toAPIToken())
	}
	return tokens, nil
}

// RevokeAPIToken retires a token. Revoking an already-revoked token succeeds:
// the caller's intent is satisfied, and an operator racing a script should not
// have to distinguish the two.
func (db *DB) RevokeAPIToken(ctx context.Context, tokenID, reason string) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	result := db.gorm.WithContext(ctx).Model(&apiTokenRecord{}).
		Where("token_id = ? AND revoked_at IS NULL", tokenID).
		Updates(map[string]any{"revoked_at": time.Now().UTC(), "revocation_reason": nullableTrimmed(reason)})
	if result.Error != nil {
		return fmt.Errorf("revoke captain API token: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		return nil
	}
	_, err := db.GetAPIToken(ctx, tokenID)
	return err
}

// toVerifierRecord hands captaintoken exactly what it needs to check a
// credential, and nothing that would tempt a caller to compare secrets itself.
func (r apiTokenRecord) toVerifierRecord() captaintoken.Record {
	record := captaintoken.Record{
		ID: r.TokenID, SecretHash: r.SecretHash, Name: r.Name, Scope: r.Scope,
		Agent: derefString(r.Agent), Pool: r.Pool, PoolAgents: slices.Clone(r.PoolAgents),
		ExpiresAt: r.ExpiresAt, RevokedAt: r.RevokedAt,
	}
	if r.MaxAgents != nil {
		record.MaxAgents = *r.MaxAgents
	}
	return record
}

func (r apiTokenRecord) toAPIToken() APIToken {
	token := APIToken{
		ID: r.ID, TokenID: r.TokenID, Name: r.Name, Scope: r.Scope,
		Agent: derefString(r.Agent), Pool: r.Pool, PoolAgents: slices.Clone(r.PoolAgents),
		ExpiresAt: r.ExpiresAt, RevokedAt: r.RevokedAt,
		RevocationReason: optionalString(r.RevocationReason),
		LastUsedAt:       r.LastUsedAt, CreatedAt: r.CreatedAt,
	}
	if r.MaxAgents != nil {
		token.MaxAgents = *r.MaxAgents
	}
	return token
}
