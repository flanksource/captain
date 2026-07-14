package database

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// EndSessionProcesses closes every still-open process row bound to a session —
// the SessionEnd hook's teardown — and returns how many rows were closed.
func (db *DB) EndSessionProcesses(ctx context.Context, sessionID uuid.UUID) (int64, error) {
	if err := db.requireGorm(); err != nil {
		return 0, err
	}
	if sessionID == uuid.Nil {
		return 0, fmt.Errorf("%w: session ID is required", ErrInvalidSessionProcess)
	}
	now := time.Now().UTC()
	result := db.gorm.WithContext(ctx).Model(&sessionProcessRecord{}).
		Where("session_id = ? AND ended_at IS NULL", sessionID).
		Updates(map[string]any{
			"ended_at": gorm.Expr("GREATEST(process_started_at, ?)", now),
			"status":   "exited",
		})
	if result.Error != nil {
		return 0, fmt.Errorf("end Captain session processes: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// EndStaleSessionProcesses closes open process rows on any host whose last
// observation predates cutoff — crash leftovers the per-host reaper cannot see
// because its ps snapshot only covers the local host.
func (db *DB) EndStaleSessionProcesses(ctx context.Context, cutoff time.Time) (int64, error) {
	if err := db.requireGorm(); err != nil {
		return 0, err
	}
	result := db.gorm.WithContext(ctx).Model(&sessionProcessRecord{}).
		Where("ended_at IS NULL").
		Where("COALESCE(last_heartbeat_at, sampled_at, process_started_at) < ?", cutoff.UTC()).
		Updates(map[string]any{
			"ended_at": gorm.Expr("GREATEST(process_started_at, ?)", time.Now().UTC()),
			"status":   "exited",
		})
	if result.Error != nil {
		return 0, fmt.Errorf("end stale Captain session processes: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// DeleteSessionSourcesByPaths prunes ingest bookkeeping for transcripts that no
// longer exist on disk. Session rows stay — only the per-file resume state goes.
func (db *DB) DeleteSessionSourcesByPaths(ctx context.Context, paths []string) (int64, error) {
	if err := db.requireGorm(); err != nil {
		return 0, err
	}
	if len(paths) == 0 {
		return 0, nil
	}
	result := db.gorm.WithContext(ctx).
		Where("path IN ?", paths).
		Delete(&sessionSourceRecord{})
	if result.Error != nil {
		return 0, fmt.Errorf("delete Captain session sources: %w", result.Error)
	}
	return result.RowsAffected, nil
}

// VacuumAnalyze reclaims dead tuples and refreshes planner statistics on the
// embedded database. VACUUM cannot run inside a transaction block, so this
// issues a bare statement.
func (db *DB) VacuumAnalyze(ctx context.Context) error {
	if err := db.requireGorm(); err != nil {
		return err
	}
	if err := db.gorm.WithContext(ctx).Exec("VACUUM ANALYZE").Error; err != nil {
		return fmt.Errorf("vacuum analyze: %w", err)
	}
	return nil
}
