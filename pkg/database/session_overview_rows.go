package database

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
)

// SessionOverviewChangeChannel is the PostgreSQL channel migration 88
// notifies when a session's overview row changes in a way a list or detail
// reader renders. The payload is the session UUID.
const SessionOverviewChangeChannel = "captain_session_overview_change"

// sessionOverviewEnsureBatch bounds how many sessions one ensure transaction
// rebuilds, so a post-crash backfill never holds every overview row lock at
// once while ingest keeps writing.
const sessionOverviewEnsureBatch = 500

// EnsureSessionOverview rebuilds the overview row of every session that has
// none, which after crash recovery (it truncates UNLOGGED tables) is all of
// them. It reports whether any row was rebuilt.
func (db *DB) EnsureSessionOverview(ctx context.Context) (bool, error) {
	if err := db.requireGorm(); err != nil {
		return false, err
	}
	rebuiltAny := false
	for {
		var rebuilt bool
		if err := db.gorm.WithContext(ctx).
			Raw(`SELECT captain_session_overview_ensure(?)`, sessionOverviewEnsureBatch).
			Scan(&rebuilt).Error; err != nil {
			return rebuiltAny, fmt.Errorf("ensure Captain session overview rows: %w", err)
		}
		if !rebuilt {
			return rebuiltAny, nil
		}
		rebuiltAny = true
	}
}

// LiveSessionDir is where a session with a live process works: the recorded
// session directory and the process's own working directory.
type LiveSessionDir struct {
	SessionID  uuid.UUID `gorm:"column:session_id" json:"sessionId"`
	CWD        *string   `gorm:"column:cwd" json:"cwd,omitempty"`
	ProcessCWD *string   `gorm:"column:process_cwd" json:"processCwd,omitempty"`
}

// ListLiveSessionDirs lists every session with a live process, most recently
// active first, excluding the same Codex auto-review sessions as the list.
func (db *DB) ListLiveSessionDirs(ctx context.Context) ([]LiveSessionDir, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var dirs []LiveSessionDir
	if err := db.gorm.WithContext(ctx).Raw(`
		SELECT o.id AS session_id, o.cwd, o.process_cwd
		FROM captain_session_overview_rows o
		WHERE o.process_active
		  AND COALESCE(o.metadata->>'model', o.model, '') <> @excluded_model
		ORDER BY o.activity_at DESC, o.id DESC`,
		sql.Named("excluded_model", api.CodexAutoReviewModel),
	).Scan(&dirs).Error; err != nil {
		return nil, fmt.Errorf("list live Captain session directories: %w", err)
	}
	return dirs, nil
}
