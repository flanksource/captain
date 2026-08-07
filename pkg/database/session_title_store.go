package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SessionTitleSource records who named a session, which is what decides whether
// a later writer is allowed to rename it.
type SessionTitleSource string

const (
	// SessionTitleDerived is a title inferred from the opening prompt. It only
	// ever fills a blank, so a real title is never demoted back to a guess.
	SessionTitleDerived SessionTitleSource = "derived"
	// SessionTitleAI is a title the agent chose for itself. It replaces a derived
	// title but never a person's.
	SessionTitleAI SessionTitleSource = "ai"
	// SessionTitleUser is an explicit rename and always wins.
	SessionTitleUser SessionTitleSource = "user"
)

// SessionTitleSourceKey is where the source is kept inside the session metadata.
const SessionTitleSourceKey = "titleSource"

type SetSessionTitleInput struct {
	ID     uuid.UUID
	Title  string
	Source SessionTitleSource
}

// SetSessionTitle renames a session when the incoming source outranks the one
// already stored (user > ai > derived). The precedence lives in the UPDATE's
// WHERE clause so concurrent writers cannot interleave a read and a write, and
// a losing write is a no-op rather than an error. Titles are not state, so this
// deliberately leaves state_version alone.
func (db *DB) SetSessionTitle(ctx context.Context, input SetSessionTitleInput) (*Session, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if input.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: session ID is required", ErrInvalidSession)
	}
	title := strings.Join(strings.Fields(input.Title), " ")
	if title == "" {
		return nil, fmt.Errorf("%w: title cannot be empty", ErrInvalidSession)
	}
	query := db.gorm.WithContext(ctx).Model(&sessionRecord{}).Where("id = ?", input.ID)
	switch input.Source {
	case SessionTitleDerived:
		query = query.Where("title IS NULL OR title = ''")
	case SessionTitleAI:
		query = query.Where("COALESCE(metadata->>?, '') <> ?", SessionTitleSourceKey, string(SessionTitleUser))
	case SessionTitleUser:
	default:
		return nil, fmt.Errorf("%w: unknown title source %q", ErrInvalidSession, input.Source)
	}
	result := query.Updates(map[string]any{
		"title": title,
		"metadata": gorm.Expr("metadata || jsonb_build_object(?::text, ?::text)",
			SessionTitleSourceKey, string(input.Source)),
	})
	if result.Error != nil {
		return nil, fmt.Errorf("set Captain session title: %w", result.Error)
	}
	return db.GetSession(ctx, input.ID)
}
