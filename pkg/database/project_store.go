package database

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// ProjectSessionAggregate contains only the session fields needed by the
// project picker, grouped by working directory and source.
type ProjectSessionAggregate struct {
	CWD            string     `gorm:"column:cwd"`
	Source         string     `gorm:"column:source"`
	SessionCount   int        `gorm:"column:session_count"`
	ProcessActive  bool       `gorm:"column:process_active"`
	LastActivityAt *time.Time `gorm:"column:last_activity_at"`
}

func (db *DB) ListProjectSessionAggregates(ctx context.Context) ([]ProjectSessionAggregate, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var rows []ProjectSessionAggregate
	err := db.gorm.WithContext(ctx).Raw(`
		WITH project_sessions AS (
			SELECT
				COALESCE(NULLIF(s.cwd, ''), process.cwd, '') AS cwd,
				s.source,
				process.id IS NOT NULL AS process_active,
				COALESCE(s.last_activity_at, s.started_at) AS last_activity_at
			FROM captain_sessions s
			LEFT JOIN LATERAL (
				SELECT p.id, p.cwd
				FROM captain_session_processes p
				WHERE p.session_id = s.id
					AND p.ended_at IS NULL
				ORDER BY COALESCE(p.last_heartbeat_at, p.process_started_at) DESC, p.id DESC
				LIMIT 1
			) process ON true
			WHERE s.parent_session_id IS NULL
				AND COALESCE(
					s.metadata->>'model',
					(
						SELECT c.model
						FROM captain_turns t
						JOIN captain_model_calls c ON c.turn_id = t.id
						WHERE t.session_id = s.id
						ORDER BY COALESCE(c.ended_at, c.started_at, c.created_at) DESC, c.call_index DESC
						LIMIT 1
					),
					''
				) <> ?
		)
		SELECT
			cwd,
			source,
			count(*) AS session_count,
			bool_or(process_active) AS process_active,
			max(last_activity_at) AS last_activity_at
		FROM project_sessions
		GROUP BY cwd, source
		ORDER BY max(last_activity_at) DESC NULLS LAST`, api.CodexAutoReviewModel).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list Captain project session aggregates: %w", err)
	}
	return rows, nil
}
