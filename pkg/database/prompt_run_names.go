package database

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type NamedPrompt struct {
	Name      string    `json:"name"`
	Origin    string    `json:"origin"`
	RunID     uuid.UUID `json:"runId"`
	SessionID uuid.UUID `json:"sessionId"`
	QueuedAt  time.Time `json:"queuedAt"`
}

func (db *DB) ListNamedPrompts(ctx context.Context) ([]NamedPrompt, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	names := []NamedPrompt{}
	err := db.gorm.WithContext(ctx).Raw(`
		SELECT DISTINCT ON (spec_profile)
			spec_profile AS name, COALESCE(origin, '') AS origin,
			id AS run_id, session_id, queued_at
		FROM captain_prompt_runs
		WHERE spec_profile IS NOT NULL AND btrim(spec_profile) <> ''
		ORDER BY spec_profile, queued_at DESC, id DESC
	`).Scan(&names).Error
	if err != nil {
		return nil, fmt.Errorf("list named prompt runs: %w", err)
	}
	return names, nil
}
