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
func (db *DB) findSessionByIdentity(ctx context.Context, record sessionRecord) (*sessionRecord, error) {
	query := db.gorm.WithContext(ctx)
	if record.ProviderSessionID != nil {
		query = query.Where("source = ? AND provider = ? AND host_id = ? AND provider_session_id = ?",
			record.Source, record.Provider, record.HostID, *record.ProviderSessionID)
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
		CLIVersion: nullableTrimmed(input.CLIVersion), LifecycleStatus: SessionLifecycleCreated,
		ActivityState: SessionActivityIdle, HealthState: SessionHealthHealthy, StateObservedAt: now,
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
	if existing.Source != record.Source || existing.Provider != record.Provider || existing.HostID != record.HostID {
		return nil, fmt.Errorf("%w: existing session has a different provider identity", ErrSessionConflict)
	}
	if callerSuppliedID && existing.ID != input.ID {
		return nil, fmt.Errorf("%w: provider identity already belongs to session %s, not caller-supplied ID %s", ErrSessionConflict, existing.ID, input.ID)
	}
	if !equalOptionalUUID(existing.ParentSessionID, record.ParentSessionID) ||
		!equalOptionalUUID(existing.RootSessionID, record.RootSessionID) {
		return nil, fmt.Errorf("%w: existing session has a different hierarchy", ErrSessionConflict)
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

type PromptRunPhase string

const (
	PromptRunPhaseQueued   PromptRunPhase = "queued"
	PromptRunPhasePreRun   PromptRunPhase = "pre_run"
	PromptRunPhaseGenerate PromptRunPhase = "generate"
	PromptRunPhaseVerify   PromptRunPhase = "verify"
	PromptRunPhaseFeedback PromptRunPhase = "feedback"
	PromptRunPhasePostRun  PromptRunPhase = "post_run"
	PromptRunPhaseOutput   PromptRunPhase = "output"
	PromptRunPhaseFinished PromptRunPhase = "finished"
)

type PromptRunState string

const (
	PromptRunStatePending   PromptRunState = "pending"
	PromptRunStateRunning   PromptRunState = "running"
	PromptRunStateWaiting   PromptRunState = "waiting"
	PromptRunStateSucceeded PromptRunState = "succeeded"
	PromptRunStateFailed    PromptRunState = "failed"
	PromptRunStateCancelled PromptRunState = "cancelled"
)

type PromptRun struct {
	ID                   uuid.UUID        `json:"id"`
	SessionID            uuid.UUID        `json:"sessionId"`
	RootSessionID        uuid.UUID        `json:"rootSessionId"`
	ExecutionSessionID   *uuid.UUID       `json:"executionSessionId,omitempty"`
	BatchID              *uuid.UUID       `json:"batchId,omitempty"`
	ParentRunID          *uuid.UUID       `json:"parentRunId,omitempty"`
	InputPlanID          *uuid.UUID       `json:"inputPlanId,omitempty"`
	InputPlanRevisionID  *uuid.UUID       `json:"inputPlanRevisionId,omitempty"`
	Origin               string           `json:"origin,omitempty"`
	SpecProfile          string           `json:"specProfile,omitempty"`
	AdmissionKey         string           `json:"admissionKey,omitempty"`
	RenderedSpec         map[string]any   `json:"renderedSpec,omitempty"`
	Runtime              PromptRunRuntime `json:"runtime,omitempty"`
	PromptMarkdown       string           `json:"promptMarkdown,omitempty"`
	VerificationMarkdown string           `json:"verificationMarkdown,omitempty"`
	Phase                PromptRunPhase   `json:"phase"`
	State                PromptRunState   `json:"state"`
	CurrentIteration     int              `json:"currentIteration"`
	ResultText           string           `json:"resultText,omitempty"`
	ResultJSON           map[string]any   `json:"resultJson,omitempty"`
	Error                string           `json:"error,omitempty"`
	Version              int64            `json:"version"`
	QueuedAt             time.Time        `json:"queuedAt"`
	StartedAt            *time.Time       `json:"startedAt,omitempty"`
	FinishedAt           *time.Time       `json:"finishedAt,omitempty"`
	CreatedAt            time.Time        `json:"createdAt"`
	UpdatedAt            time.Time        `json:"updatedAt"`
}

// PromptRunFilter limits ListPromptRuns. Nil fields are not filtered.
type PromptRunFilter struct {
	SessionID *uuid.UUID
	State     *PromptRunState
}

type CreatePromptRunInput struct {
	ID                   uuid.UUID
	SessionID            uuid.UUID
	RootSessionID        *uuid.UUID
	ExecutionSessionID   *uuid.UUID
	BatchID              *uuid.UUID
	ParentRunID          *uuid.UUID
	InputPlanID          *uuid.UUID
	InputPlanRevisionID  *uuid.UUID
	Origin               string
	SpecProfile          string
	AdmissionKey         string
	RenderedSpec         map[string]any
	Runtime              PromptRunRuntime
	PromptMarkdown       string
	VerificationMarkdown string
}

type UpdatePromptRunInput struct {
	ID                 uuid.UUID
	ExpectedVersion    int64
	Phase              *PromptRunPhase
	State              *PromptRunState
	CurrentIteration   *int
	ExecutionSessionID *uuid.UUID
	RenderedSpec       *map[string]any
	Runtime            *PromptRunRuntime
	ResultText         *string
	ResultJSON         *map[string]any
	Error              *string
}

type promptRunRecord struct {
	ID                   uuid.UUID        `gorm:"column:id;type:uuid;primaryKey"`
	SessionID            uuid.UUID        `gorm:"column:session_id;type:uuid"`
	RootSessionID        uuid.UUID        `gorm:"column:root_session_id;type:uuid"`
	ExecutionSessionID   *uuid.UUID       `gorm:"column:execution_session_id;type:uuid"`
	BatchID              *uuid.UUID       `gorm:"column:batch_id;type:uuid"`
	ParentRunID          *uuid.UUID       `gorm:"column:parent_run_id;type:uuid"`
	InputPlanID          *uuid.UUID       `gorm:"column:input_plan_id;type:uuid"`
	InputPlanRevisionID  *uuid.UUID       `gorm:"column:input_plan_revision_id;type:uuid"`
	Origin               *string          `gorm:"column:origin"`
	SpecProfile          *string          `gorm:"column:spec_profile"`
	AdmissionKey         *string          `gorm:"column:admission_key"`
	RenderedSpec         map[string]any   `gorm:"column:rendered_spec;serializer:json;type:jsonb"`
	Runtime              PromptRunRuntime `gorm:"column:runtime;serializer:json;type:jsonb"`
	PromptMarkdown       *string          `gorm:"column:prompt_markdown"`
	VerificationMarkdown *string          `gorm:"column:verification_markdown"`
	Phase                PromptRunPhase   `gorm:"column:phase"`
	State                PromptRunState   `gorm:"column:state"`
	CurrentIteration     int              `gorm:"column:current_iteration"`
	ResultText           *string          `gorm:"column:result_text"`
	ResultJSON           map[string]any   `gorm:"column:result_json;serializer:json;type:jsonb"`
	Error                *string          `gorm:"column:error"`
	Version              int64            `gorm:"column:version"`
	QueuedAt             time.Time        `gorm:"column:queued_at"`
	StartedAt            *time.Time       `gorm:"column:started_at"`
	FinishedAt           *time.Time       `gorm:"column:finished_at"`
	CreatedAt            time.Time        `gorm:"column:created_at"`
	UpdatedAt            time.Time        `gorm:"column:updated_at"`
}

func (promptRunRecord) TableName() string { return "captain_prompt_runs" }

// CreatePromptRun creates a run and returns its authoritative UUID. AdmissionKey
// or a caller-supplied ID makes retries idempotent.
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
		ID: input.ID, SessionID: input.SessionID, RootSessionID: rootID, ExecutionSessionID: input.ExecutionSessionID, BatchID: input.BatchID,
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

func sessionFromRecord(record sessionRecord) Session {
	return Session{
		ID: record.ID, ProviderSessionID: optionalString(record.ProviderSessionID), Source: record.Source,
		Provider: record.Provider, HostID: record.HostID, ParentSessionID: record.ParentSessionID,
		RootSessionID: record.RootSessionID, Path: optionalString(record.Path), Project: optionalString(record.Project),
		CWD: optionalString(record.CWD), Title: optionalString(record.Title), InitialPrompt: optionalString(record.InitialPrompt),
		Slug: optionalString(record.Slug), AgentType: optionalString(record.AgentType), Description: optionalString(record.Description),
		CLIVersion: optionalString(record.CLIVersion), LifecycleStatus: record.LifecycleStatus,
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

func promptRunFromRecord(record promptRunRecord) PromptRun {
	return PromptRun{
		ID: record.ID, SessionID: record.SessionID, RootSessionID: record.RootSessionID, ExecutionSessionID: record.ExecutionSessionID, BatchID: record.BatchID,
		ParentRunID: record.ParentRunID, InputPlanID: record.InputPlanID, InputPlanRevisionID: record.InputPlanRevisionID,
		Origin: optionalString(record.Origin), SpecProfile: optionalString(record.SpecProfile), AdmissionKey: optionalString(record.AdmissionKey),
		RenderedSpec: record.RenderedSpec, Runtime: record.Runtime, PromptMarkdown: optionalString(record.PromptMarkdown),
		VerificationMarkdown: optionalString(record.VerificationMarkdown), Phase: record.Phase, State: record.State,
		CurrentIteration: record.CurrentIteration, ResultText: optionalString(record.ResultText), ResultJSON: record.ResultJSON,
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
	if execution.ParentSessionID != nil || execution.RootSessionID != nil {
		return fmt.Errorf("%w: execution session %s is not a root provider thread", ErrPromptRunConflict, execution.ID)
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

type sqlStateError interface {
	SQLState() string
}

func isUniqueViolation(err error) bool {
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return true
	}
	var sqlState sqlStateError
	return errors.As(err, &sqlState) && sqlState.SQLState() == "23505"
}
