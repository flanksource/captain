package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalidSession    = errors.New("invalid Captain session")
	ErrSessionNotFound   = errors.New("captain session not found")
	ErrSessionConflict   = errors.New("captain session conflict")
	ErrInvalidPromptRun  = errors.New("invalid Captain prompt run")
	ErrPromptRunNotFound = errors.New("captain prompt run not found")
	ErrPromptRunConflict = errors.New("captain prompt run conflict")
)

type SessionLifecycleStatus string

const (
	SessionLifecycleCreated     SessionLifecycleStatus = "created"
	SessionLifecycleRunning     SessionLifecycleStatus = "running"
	SessionLifecycleSucceeded   SessionLifecycleStatus = "succeeded"
	SessionLifecycleFailed      SessionLifecycleStatus = "failed"
	SessionLifecycleCancelled   SessionLifecycleStatus = "cancelled"
	SessionLifecycleInterrupted SessionLifecycleStatus = "interrupted"
)

type SessionActivityState string

const (
	SessionActivityIdle     SessionActivityState = "idle"
	SessionActivityThinking SessionActivityState = "thinking"
	SessionActivityWorking  SessionActivityState = "working"
	SessionActivityAsk      SessionActivityState = "ask"
	SessionActivityApproval SessionActivityState = "approval"
)

type SessionHealthState string

const (
	SessionHealthHealthy SessionHealthState = "healthy"
	SessionHealthStalled SessionHealthState = "stalled"
	SessionHealthZombie  SessionHealthState = "zombie"
)

// Session is Captain's authoritative provider-session identity.
type Session struct {
	ID                uuid.UUID              `json:"id"`
	ProviderSessionID string                 `json:"providerSessionId,omitempty"`
	Source            string                 `json:"source"`
	Provider          string                 `json:"provider"`
	HostID            string                 `json:"hostId"`
	ParentSessionID   *uuid.UUID             `json:"parentSessionId,omitempty"`
	RootSessionID     *uuid.UUID             `json:"rootSessionId,omitempty"`
	Path              string                 `json:"path,omitempty"`
	Project           string                 `json:"project,omitempty"`
	CWD               string                 `json:"cwd,omitempty"`
	Title             string                 `json:"title,omitempty"`
	InitialPrompt     string                 `json:"initialPrompt,omitempty"`
	Slug              string                 `json:"slug,omitempty"`
	AgentType         string                 `json:"agentType,omitempty"`
	Description       string                 `json:"description,omitempty"`
	CLIVersion        string                 `json:"cliVersion,omitempty"`
	Metadata          map[string]any         `json:"metadata,omitempty"`
	LifecycleStatus   SessionLifecycleStatus `json:"lifecycleStatus"`
	ActivityState     SessionActivityState   `json:"activityState"`
	HealthState       SessionHealthState     `json:"healthState"`
	StateReason       string                 `json:"stateReason,omitempty"`
	StateVersion      int64                  `json:"stateVersion"`
	StateObservedAt   time.Time              `json:"stateObservedAt"`
	StartedAt         *time.Time             `json:"startedAt,omitempty"`
	EndedAt           *time.Time             `json:"endedAt,omitempty"`
	CreatedAt         time.Time              `json:"createdAt"`
	UpdatedAt         time.Time              `json:"updatedAt"`
}

type CreateSessionInput struct {
	ID                uuid.UUID
	ProviderSessionID string
	Source            string
	Provider          string
	HostID            string
	ParentSessionID   *uuid.UUID
	RootSessionID     *uuid.UUID
	Path              string
	Project           string
	CWD               string
	Title             string
	InitialPrompt     string
	Slug              string
	AgentType         string
	Description       string
	CLIVersion        string
	Metadata          map[string]any
}

type sessionRecord struct {
	ID                uuid.UUID              `gorm:"column:id;type:uuid;primaryKey"`
	ProviderSessionID *string                `gorm:"column:provider_session_id"`
	Source            string                 `gorm:"column:source"`
	Provider          string                 `gorm:"column:provider"`
	HostID            string                 `gorm:"column:host_id"`
	ParentSessionID   *uuid.UUID             `gorm:"column:parent_session_id;type:uuid"`
	RootSessionID     *uuid.UUID             `gorm:"column:root_session_id;type:uuid"`
	Path              *string                `gorm:"column:path"`
	Project           *string                `gorm:"column:project"`
	CWD               *string                `gorm:"column:cwd"`
	Title             *string                `gorm:"column:title"`
	InitialPrompt     *string                `gorm:"column:initial_prompt"`
	Slug              *string                `gorm:"column:slug"`
	AgentType         *string                `gorm:"column:agent_type"`
	Description       *string                `gorm:"column:description"`
	CLIVersion        *string                `gorm:"column:cli_version"`
	Metadata          map[string]any         `gorm:"column:metadata;serializer:json;type:jsonb"`
	LifecycleStatus   SessionLifecycleStatus `gorm:"column:lifecycle_status"`
	ActivityState     SessionActivityState   `gorm:"column:activity_state"`
	HealthState       SessionHealthState     `gorm:"column:health_state"`
	StateReason       *string                `gorm:"column:state_reason"`
	StateVersion      int64                  `gorm:"column:state_version"`
	StateObservedAt   time.Time              `gorm:"column:state_observed_at"`
	StartedAt         *time.Time             `gorm:"column:started_at"`
	EndedAt           *time.Time             `gorm:"column:ended_at"`
	CreatedAt         time.Time              `gorm:"column:created_at"`
	UpdatedAt         time.Time              `gorm:"column:updated_at"`
}

func (sessionRecord) TableName() string { return "captain_sessions" }

// findSessionByIdentity resolves the row a create would collide with — the
// provider identity when one is supplied, otherwise the ID itself — and returns
// nil when no such row exists.
//
// provider is deliberately not part of the lookup, and matches
// captain_sessions_provider_identity_key. It is a label three writers spell
// differently for one Codex rollout (`openai`, `codex-agent`, or empty), so matching
// on it meant none of them ever found the others and each inserted its own row
// for the same transcript.
func (db *DB) findSessionByIdentity(ctx context.Context, record sessionRecord) (*sessionRecord, error) {
	query := db.gorm.WithContext(ctx)
	if record.ProviderSessionID != nil {
		query = query.Where("source = ? AND host_id = ? AND provider_session_id = ?",
			record.Source, record.HostID, *record.ProviderSessionID)
	} else {
		query = query.Where("id = ?", record.ID)
	}
	var existing sessionRecord
	if err := query.First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("read existing Captain session: %w", err)
	}
	return &existing, nil
}

func (db *DB) CreateOrGetSession(ctx context.Context, input CreateSessionInput) (*Session, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	input.Source = strings.TrimSpace(input.Source)
	if input.Source == "" {
		return nil, fmt.Errorf("%w: source is required", ErrInvalidSession)
	}
	input.Provider = strings.TrimSpace(input.Provider)
	input.HostID = strings.TrimSpace(input.HostID)
	if input.HostID == "" {
		input.HostID = "local"
	}
	callerSuppliedID := input.ID != uuid.Nil
	if !callerSuppliedID {
		input.ID = uuid.New()
	}
	if input.ParentSessionID != nil {
		if *input.ParentSessionID == uuid.Nil || *input.ParentSessionID == input.ID {
			return nil, fmt.Errorf("%w: session cannot have an empty or self parent", ErrInvalidSession)
		}
		parent, err := db.GetSession(ctx, *input.ParentSessionID)
		if err != nil {
			return nil, err
		}
		derivedRoot := parent.ID
		if parent.RootSessionID != nil {
			derivedRoot = *parent.RootSessionID
		}
		if input.RootSessionID != nil && *input.RootSessionID != derivedRoot {
			return nil, fmt.Errorf("%w: root session %s does not match parent aggregate root %s", ErrSessionConflict, *input.RootSessionID, derivedRoot)
		}
		input.RootSessionID = &derivedRoot
	} else if input.RootSessionID != nil {
		if *input.RootSessionID == uuid.Nil {
			return nil, fmt.Errorf("%w: root session ID cannot be empty", ErrInvalidSession)
		}
		if *input.RootSessionID == input.ID {
			return nil, fmt.Errorf("%w: session cannot be its own root reference", ErrInvalidSession)
		}
		root, err := db.GetSession(ctx, *input.RootSessionID)
		if err != nil {
			return nil, err
		}
		if root.ParentSessionID != nil || root.RootSessionID != nil {
			return nil, fmt.Errorf("%w: root session %s is not a canonical aggregate root", ErrSessionConflict, root.ID)
		}
	}
	if input.RootSessionID != nil && *input.RootSessionID == input.ID {
		return nil, fmt.Errorf("%w: session cannot be its own root reference", ErrInvalidSession)
	}
	now := time.Now().UTC()
	record := sessionRecord{
		ID: input.ID, ProviderSessionID: nullableTrimmed(input.ProviderSessionID), Source: input.Source,
		Provider: input.Provider, HostID: input.HostID, ParentSessionID: input.ParentSessionID,
		RootSessionID: input.RootSessionID, Path: nullableTrimmed(input.Path), Project: nullableTrimmed(input.Project),
		CWD: nullableTrimmed(normalizeCWD(input.CWD)), Title: nullableTrimmed(input.Title), InitialPrompt: nullableTrimmed(input.InitialPrompt),
		Slug: nullableTrimmed(input.Slug), AgentType: nullableTrimmed(input.AgentType), Description: nullableTrimmed(input.Description),
		CLIVersion: nullableTrimmed(input.CLIVersion), Metadata: input.Metadata, LifecycleStatus: SessionLifecycleCreated,
		ActivityState: SessionActivityIdle, HealthState: SessionHealthHealthy, StateObservedAt: now,
	}
	if record.Metadata == nil {
		record.Metadata = map[string]any{}
	}
	// Read before write. Callers re-get far more often than they create — a
	// monitor asks for the same session on every transcript append — and an
	// unconditional insert makes each of those a speculative-insertion conflict
	// that waits on whichever backend currently holds the row. The create below
	// stays as the race-safe fallback, it is just no longer the common path.
	var existing *sessionRecord
	// A generated ID with no provider identity cannot match an existing row.
	if callerSuppliedID || record.ProviderSessionID != nil {
		found, err := db.findSessionByIdentity(ctx, record)
		if err != nil {
			return nil, err
		}
		existing = found
	}
	if existing == nil {
		result := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
		if result.Error != nil {
			return nil, fmt.Errorf("create Captain session: %w", result.Error)
		}
		if result.RowsAffected == 1 {
			return db.GetSession(ctx, record.ID)
		}
		found, err := db.findSessionByIdentity(ctx, record)
		if err != nil {
			return nil, err
		}
		if found == nil {
			return nil, fmt.Errorf("%w: create was rejected by a conflicting identity", ErrSessionConflict)
		}
		existing = found
	}
	if existing.Source != record.Source || existing.HostID != record.HostID {
		return nil, fmt.Errorf("%w: existing session has a different provider identity", ErrSessionConflict)
	}
	// The label is filled in by whichever writer first knows a concrete one. A
	// later writer disagreeing about it is not a conflict — it is the same
	// transcript described from a different vantage point.
	if existing.Provider == "" && record.Provider != "" {
		if err := db.gorm.WithContext(ctx).Model(&sessionRecord{}).
			Where("id = ? AND provider = ''", existing.ID).
			Update("provider", record.Provider).Error; err != nil {
			return nil, fmt.Errorf("adopt Captain session provider label: %w", err)
		}
	}
	if callerSuppliedID && existing.ID != input.ID {
		return nil, fmt.Errorf("%w: provider identity already belongs to session %s, not caller-supplied ID %s", ErrSessionConflict, existing.ID, input.ID)
	}
	if err := db.reconcileSessionHierarchy(ctx, existing, record); err != nil {
		return nil, err
	}
	return db.GetSession(ctx, existing.ID)
}

type UpdateSessionStateInput struct {
	ID                uuid.UUID
	ExpectedVersion   int64
	ProviderSessionID *string
	LifecycleStatus   *SessionLifecycleStatus
	ActivityState     *SessionActivityState
	HealthState       *SessionHealthState
	StateReason       *string
}

// UpdateSessionState applies session identity/state projection changes only at
// ExpectedVersion. ProviderSessionID is a set-once binding: concurrent binders
// may set NULL to one non-empty value, while exact retries remain idempotent.
// Captain's trigger advances StateVersion for state changes.
func (db *DB) UpdateSessionState(ctx context.Context, input UpdateSessionStateInput) (*Session, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if input.ID == uuid.Nil || input.ExpectedVersion < 0 {
		return nil, fmt.Errorf("%w: ID and a nonnegative expected version are required", ErrInvalidSession)
	}
	updates := map[string]any{}
	var distinctPredicates []string
	var distinctArgs []any
	query := db.gorm.WithContext(ctx).Model(&sessionRecord{}).
		Where("id = ? AND state_version = ?", input.ID, input.ExpectedVersion)
	if input.ProviderSessionID != nil {
		providerSessionID := strings.TrimSpace(*input.ProviderSessionID)
		if providerSessionID == "" {
			return nil, fmt.Errorf("%w: provider session ID cannot be cleared or empty", ErrInvalidSession)
		}
		updates["provider_session_id"] = providerSessionID
		query = query.Where("(provider_session_id IS NULL OR provider_session_id = ?)", providerSessionID)
		distinctPredicates = append(distinctPredicates, "provider_session_id IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, providerSessionID)
	}
	if input.LifecycleStatus != nil {
		if !validSessionLifecycle(*input.LifecycleStatus) {
			return nil, fmt.Errorf("%w: unknown lifecycle status %q", ErrInvalidSession, *input.LifecycleStatus)
		}
		updates["lifecycle_status"] = *input.LifecycleStatus
		distinctPredicates = append(distinctPredicates, "lifecycle_status IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, *input.LifecycleStatus)
	}
	if input.ActivityState != nil {
		if !validSessionActivity(*input.ActivityState) {
			return nil, fmt.Errorf("%w: unknown activity state %q", ErrInvalidSession, *input.ActivityState)
		}
		updates["activity_state"] = *input.ActivityState
		distinctPredicates = append(distinctPredicates, "activity_state IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, *input.ActivityState)
	}
	if input.HealthState != nil {
		if !validSessionHealth(*input.HealthState) {
			return nil, fmt.Errorf("%w: unknown health state %q", ErrInvalidSession, *input.HealthState)
		}
		updates["health_state"] = *input.HealthState
		distinctPredicates = append(distinctPredicates, "health_state IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, *input.HealthState)
	}
	if input.StateReason != nil {
		stateReason := nullableTrimmed(*input.StateReason)
		updates["state_reason"] = stateReason
		distinctPredicates = append(distinctPredicates, "state_reason IS DISTINCT FROM ?")
		distinctArgs = append(distinctArgs, stateReason)
	}
	if len(updates) == 0 {
		return nil, fmt.Errorf("%w: no update fields supplied", ErrInvalidSession)
	}
	query = query.Where("("+strings.Join(distinctPredicates, " OR ")+")", distinctArgs...)
	result := query.Updates(updates)
	if result.Error != nil {
		if isUniqueViolation(result.Error) {
			return nil, fmt.Errorf("%w: provider session identity is already bound", ErrSessionConflict)
		}
		return nil, fmt.Errorf("update Captain session state: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		var current sessionRecord
		if err := db.gorm.WithContext(ctx).First(&current, "id = ?", input.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, input.ID)
			}
			return nil, fmt.Errorf("check Captain session: %w", err)
		}
		if input.ProviderSessionID != nil && current.ProviderSessionID != nil &&
			*current.ProviderSessionID != strings.TrimSpace(*input.ProviderSessionID) {
			return nil, fmt.Errorf("%w: provider session ID is already bound to %q", ErrSessionConflict, *current.ProviderSessionID)
		}
		if current.StateVersion != input.ExpectedVersion {
			return nil, fmt.Errorf("%w: session %s is no longer at state version %d", ErrSessionConflict, input.ID, input.ExpectedVersion)
		}
		out := sessionFromRecord(current)
		return &out, nil
	}
	return db.GetSession(ctx, input.ID)
}

func (db *DB) GetSession(ctx context.Context, id uuid.UUID) (*Session, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var record sessionRecord
	if err := db.gorm.WithContext(ctx).First(&record, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
		}
		return nil, fmt.Errorf("get Captain session: %w", err)
	}
	out := sessionFromRecord(record)
	return &out, nil
}

// GetSessionByIdentity resolves either an authoritative UUID or a provider
// session ID. Provider IDs are required to be unambiguous across the supplied
// optional source/provider/host filters.
func (db *DB) GetSessionByIdentity(ctx context.Context, identity, source, provider, hostID string) (*Session, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return nil, fmt.Errorf("%w: identity is required", ErrInvalidSession)
	}
	source = strings.TrimSpace(source)
	provider = strings.TrimSpace(provider)
	hostID = strings.TrimSpace(hostID)
	if parsed, err := uuid.Parse(identity); err == nil {
		var record sessionRecord
		err := filterSessionIdentity(db.gorm.WithContext(ctx), source, provider, hostID).
			First(&record, "id = ?", parsed).Error
		if err == nil {
			out := sessionFromRecord(record)
			return &out, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("resolve Captain session UUID: %w", err)
		}
	}
	query := filterSessionIdentity(db.gorm.WithContext(ctx), source, provider, hostID).
		Where("provider_session_id = ?", identity)
	var records []sessionRecord
	if err := query.Limit(2).Find(&records).Error; err != nil {
		return nil, fmt.Errorf("resolve Captain session identity: %w", err)
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, identity)
	}
	if len(records) > 1 {
		return nil, fmt.Errorf("%w: provider session ID %q is ambiguous", ErrSessionConflict, identity)
	}
	out := sessionFromRecord(records[0])
	return &out, nil
}

func filterSessionIdentity(query *gorm.DB, source, provider, hostID string) *gorm.DB {
	if source != "" {
		query = query.Where("source = ?", source)
	}
	if provider != "" {
		query = query.Where("provider = ?", provider)
	}
	if hostID != "" {
		query = query.Where("host_id = ?", hostID)
	}
	return query
}

func sessionFromRecord(record sessionRecord) Session {
	return Session{
		ID: record.ID, ProviderSessionID: optionalString(record.ProviderSessionID), Source: record.Source,
		Provider: record.Provider, HostID: record.HostID, ParentSessionID: record.ParentSessionID,
		RootSessionID: record.RootSessionID, Path: optionalString(record.Path), Project: optionalString(record.Project),
		CWD: optionalString(record.CWD), Title: optionalString(record.Title), InitialPrompt: optionalString(record.InitialPrompt),
		Slug: optionalString(record.Slug), AgentType: optionalString(record.AgentType), Description: optionalString(record.Description),
		CLIVersion: optionalString(record.CLIVersion), Metadata: record.Metadata, LifecycleStatus: record.LifecycleStatus,
		ActivityState: record.ActivityState, HealthState: record.HealthState, StateReason: optionalString(record.StateReason),
		StateVersion: record.StateVersion, StateObservedAt: record.StateObservedAt, StartedAt: record.StartedAt,
		EndedAt: record.EndedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func validSessionLifecycle(value SessionLifecycleStatus) bool {
	switch value {
	case SessionLifecycleCreated, SessionLifecycleRunning, SessionLifecycleSucceeded,
		SessionLifecycleFailed, SessionLifecycleCancelled, SessionLifecycleInterrupted,
		SessionLifecyclePartial:
		return true
	default:
		return false
	}
}

func validSessionActivity(value SessionActivityState) bool {
	switch value {
	case SessionActivityIdle, SessionActivityThinking, SessionActivityWorking,
		SessionActivityAsk, SessionActivityApproval:
		return true
	default:
		return false
	}
}

func validSessionHealth(value SessionHealthState) bool {
	switch value {
	case SessionHealthHealthy, SessionHealthStalled, SessionHealthZombie:
		return true
	default:
		return false
	}
}
