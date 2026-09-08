package database

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type UpdateSessionStateInput struct {
	ID                uuid.UUID
	ExpectedVersion   int64
	ProviderSessionID *string
	// CWD is the directory the agent actually runs in, which is not always the
	// directory the session was booked against: a run given a git worktree works
	// somewhere the launcher only learns after setup. It carries the same meaning
	// as the column transcript ingest projects (see projectSessionColumns), so
	// the two writers agree and either may be the one that observes it first.
	CWD             *string
	LifecycleStatus *SessionLifecycleStatus
	ActivityState   *SessionActivityState
	HealthState     *SessionHealthState
	StateReason     *string
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
	change, err := newSessionStateChange(input)
	if err != nil {
		return nil, err
	}
	query := db.gorm.WithContext(ctx).Model(&sessionRecord{}).
		Where("id = ? AND state_version = ?", input.ID, input.ExpectedVersion)
	if input.ProviderSessionID != nil {
		query = query.Where("(provider_session_id IS NULL OR provider_session_id = ?)",
			change.updates["provider_session_id"])
	}
	result := query.
		Where("("+strings.Join(change.distinctPredicates, " OR ")+")", change.distinctArgs...).
		Updates(change.updates)
	if result.Error != nil {
		if isUniqueViolation(result.Error) {
			return nil, fmt.Errorf("%w: provider session identity is already bound", ErrSessionConflict)
		}
		return nil, fmt.Errorf("update Captain session state: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return db.explainUnchangedSessionState(ctx, input)
	}
	return db.GetSession(ctx, input.ID)
}

// sessionStateChange is one UpdateSessionState statement: the column
// assignments, plus the "at least one of these actually differs" guard that
// keeps an idempotent retry from advancing the state version.
type sessionStateChange struct {
	updates            map[string]any
	distinctPredicates []string
	distinctArgs       []any
}

func (c *sessionStateChange) set(column string, value any) {
	c.updates[column] = value
	c.distinctPredicates = append(c.distinctPredicates, column+" IS DISTINCT FROM ?")
	c.distinctArgs = append(c.distinctArgs, value)
}

func newSessionStateChange(input UpdateSessionStateInput) (*sessionStateChange, error) {
	change := &sessionStateChange{updates: map[string]any{}}
	if input.ProviderSessionID != nil {
		providerSessionID := strings.TrimSpace(*input.ProviderSessionID)
		if providerSessionID == "" {
			return nil, fmt.Errorf("%w: provider session ID cannot be cleared or empty", ErrInvalidSession)
		}
		change.set("provider_session_id", providerSessionID)
	}
	if input.CWD != nil {
		cwd := normalizeCWD(*input.CWD)
		if cwd == "" {
			return nil, fmt.Errorf("%w: working directory cannot be cleared or empty", ErrInvalidSession)
		}
		change.set("cwd", cwd)
	}
	if input.LifecycleStatus != nil {
		if !validSessionLifecycle(*input.LifecycleStatus) {
			return nil, fmt.Errorf("%w: unknown lifecycle status %q", ErrInvalidSession, *input.LifecycleStatus)
		}
		change.set("lifecycle_status", *input.LifecycleStatus)
	}
	if input.ActivityState != nil {
		if !validSessionActivity(*input.ActivityState) {
			return nil, fmt.Errorf("%w: unknown activity state %q", ErrInvalidSession, *input.ActivityState)
		}
		change.set("activity_state", *input.ActivityState)
	}
	if input.HealthState != nil {
		if !validSessionHealth(*input.HealthState) {
			return nil, fmt.Errorf("%w: unknown health state %q", ErrInvalidSession, *input.HealthState)
		}
		change.set("health_state", *input.HealthState)
	}
	if input.StateReason != nil {
		change.set("state_reason", nullableTrimmed(*input.StateReason))
	}
	if len(change.updates) == 0 {
		return nil, fmt.Errorf("%w: no update fields supplied", ErrInvalidSession)
	}
	return change, nil
}

// explainUnchangedSessionState decides what "no rows updated" meant. It is
// either a lost optimistic-concurrency race, a provider identity already bound
// elsewhere, or an idempotent retry — and only the last one is a success.
func (db *DB) explainUnchangedSessionState(ctx context.Context, input UpdateSessionStateInput) (*Session, error) {
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
