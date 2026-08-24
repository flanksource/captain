package database

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SessionStorageStats combines exact live-row page occupancy with PostgreSQL's
// cumulative update and vacuum counters. It is read-only and does not require
// optional extensions such as pgstattuple.
type SessionStorageStats struct {
	CapturedAt           time.Time  `json:"capturedAt"`
	HeapBytes            int64      `json:"heapBytes"`
	HeapPages            int64      `json:"heapPages"`
	IndexBytes           int64      `json:"indexBytes"`
	TotalBytes           int64      `json:"totalBytes"`
	LiveRows             int64      `json:"liveRows"`
	PagesWithLiveRows    int64      `json:"pagesWithLiveRows"`
	PagesWithoutLiveRows int64      `json:"pagesWithoutLiveRows"`
	LiveTupleBytes       int64      `json:"liveTupleBytes"`
	EstimatedLiveTuples  int64      `json:"estimatedLiveTuples"`
	EstimatedDeadTuples  int64      `json:"estimatedDeadTuples"`
	TupleUpdates         int64      `json:"tupleUpdates"`
	HOTTupleUpdates      int64      `json:"hotTupleUpdates"`
	LastAutovacuum       *time.Time `json:"lastAutovacuum,omitempty"`
	LastAutoanalyze      *time.Time `json:"lastAutoanalyze,omitempty"`
}

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

// SessionStorageStats reports a repeatable bloat signal without changing the
// database. PagesWithoutLiveRows are heap pages containing no currently visible
// session tuple; PostgreSQL may reuse them, but only a table rewrite shrinks the
// relation file itself.
func (db *DB) SessionStorageStats(ctx context.Context) (SessionStorageStats, error) {
	if err := db.requireGorm(); err != nil {
		return SessionStorageStats{}, err
	}
	var stats SessionStorageStats
	err := db.gorm.WithContext(ctx).Raw(`
		WITH live AS (
			SELECT count(*)::bigint AS live_rows,
			       count(DISTINCT split_part(trim(both '()' FROM ctid::text), ',', 1)::bigint)::bigint AS pages_with_live_rows,
			       COALESCE(sum(pg_column_size(s)), 0)::bigint AS live_tuple_bytes
			FROM captain_sessions AS s
		), sizes AS (
			SELECT pg_relation_size('captain_sessions'::regclass)::bigint AS heap_bytes,
			       pg_indexes_size('captain_sessions'::regclass)::bigint AS index_bytes,
			       pg_total_relation_size('captain_sessions'::regclass)::bigint AS total_bytes,
			       current_setting('block_size')::bigint AS block_size
		)
		SELECT statement_timestamp() AS captured_at,
		       sizes.heap_bytes,
		       sizes.heap_bytes / sizes.block_size AS heap_pages,
		       sizes.index_bytes,
		       sizes.total_bytes,
		       live.live_rows,
		       live.pages_with_live_rows,
		       sizes.heap_bytes / sizes.block_size - live.pages_with_live_rows AS pages_without_live_rows,
		       live.live_tuple_bytes,
		       table_stats.n_live_tup AS estimated_live_tuples,
		       table_stats.n_dead_tup AS estimated_dead_tuples,
		       table_stats.n_tup_upd AS tuple_updates,
		       table_stats.n_tup_hot_upd AS hot_tuple_updates,
		       table_stats.last_autovacuum,
		       table_stats.last_autoanalyze
		FROM live
		CROSS JOIN sizes
		JOIN pg_catalog.pg_stat_user_tables AS table_stats
		  ON table_stats.relid = 'captain_sessions'::regclass`).Scan(&stats).Error
	if err != nil {
		return SessionStorageStats{}, fmt.Errorf("measure Captain session storage: %w", err)
	}
	return stats, nil
}
