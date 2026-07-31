package database

import (
	"database/sql"
	"testing"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openLastActivityTestDB(t *testing.T) *DB {
	t.Helper()
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_last_activity"})
	db, err := Open(t.Context(), WithDSN(handle.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func lastActivityAt(t *testing.T, db *DB, sessionID uuid.UUID) *time.Time {
	t.Helper()
	var got sql.NullTime
	require.NoError(t, db.gorm.WithContext(t.Context()).Raw(
		`SELECT last_activity_at FROM captain_sessions WHERE id = ?`, sessionID).Scan(&got).Error)
	if !got.Valid {
		return nil
	}
	return &got.Time
}

type sessionTupleVersion struct {
	Xmin           string
	LastActivityAt time.Time
}

func readSessionTupleVersion(t *testing.T, db *DB, sessionID uuid.UUID) sessionTupleVersion {
	t.Helper()
	var version sessionTupleVersion
	require.NoError(t, db.gorm.WithContext(t.Context()).Raw(
		`SELECT xmin::text AS xmin, last_activity_at FROM captain_sessions WHERE id = ?`, sessionID).
		Scan(&version).Error)
	return version
}

// transcriptBatch mirrors what the monitor actually ingests. The shared
// testIngestBatch omits model-call started_at, but real ingest always derives it
// from the turn (pkg/monitor/ingest.go:208) -- and captain_set_model_call_state
// backfills a missing started_at with clock_timestamp(), which would read as
// fresh activity for a months-old transcript.
func transcriptBatch(modTime time.Time, byteOffset int64) IngestTranscriptInput {
	batch := testIngestBatch(modTime, byteOffset)
	for i := range batch.Turns {
		if batch.Turns[i].Call != nil {
			batch.Turns[i].Call.StartedAt = batch.Turns[i].StartedAt
			batch.Turns[i].Call.EndedAt = batch.Turns[i].EndedAt
		}
	}
	return batch
}

// pinLastActivity forces last_activity_at to a known value, bypassing the
// monotonic trigger so the tests can assert against a fixed baseline.
func pinLastActivity(t *testing.T, db *DB, sessionID uuid.UUID, at time.Time) {
	t.Helper()
	require.NoError(t, db.gorm.WithContext(t.Context()).Exec(
		`UPDATE captain_sessions SET last_activity_at = ? WHERE id = ?`, at, sessionID).Error)
}

// Declaring a session dead must never be recorded as the session doing work.
// The activity write is monotonic, so a reaper-set timestamp could never be
// corrected -- and it would make the "live process, stale activity" idle check
// unreachable by construction.
func TestLastActivityAt_ReapersDoNotCountAsActivity(t *testing.T) {
	db := openLastActivityTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	baseline := now.Add(-24 * time.Hour)

	t.Run("EndSessionProcesses", func(t *testing.T) {
		s := seedMaintenanceSession(t, db, "0195c1de-4ab8-7000-8000-0000000ac001")
		seedProcess(t, db, s.ID, 7001, now)
		pinLastActivity(t, db, s.ID, baseline)

		closed, err := db.EndSessionProcesses(t.Context(), s.ID)
		require.NoError(t, err)
		require.EqualValues(t, 1, closed, "reaper must actually close the row")

		got := lastActivityAt(t, db, s.ID)
		require.NotNil(t, got)
		assert.True(t, got.Equal(baseline), "last_activity_at = %v, want untouched %v", got, baseline)
	})

	t.Run("EndStaleSessionProcesses", func(t *testing.T) {
		s := seedMaintenanceSession(t, db, "0195c1de-4ab8-7000-8000-0000000ac002")
		seedProcess(t, db, s.ID, 7002, now.Add(-2*time.Hour))
		pinLastActivity(t, db, s.ID, baseline)

		closed, err := db.EndStaleSessionProcesses(t.Context(), now.Add(-time.Hour))
		require.NoError(t, err)
		require.EqualValues(t, 1, closed, "reaper must actually close the row")

		got := lastActivityAt(t, db, s.ID)
		require.NotNil(t, got)
		assert.True(t, got.Equal(baseline), "last_activity_at = %v, want untouched %v", got, baseline)
	})
}

// A parserVersion bump re-ingests every transcript. Session-source bookkeeping
// has no activity column, so without the allowlist its activity_at falls through
// to updated_at = now and marks every session freshly active -- permanently.
func TestLastActivityAt_ParserVersionBumpIsNotActivity(t *testing.T) {
	db := openLastActivityTestDB(t)
	modTime := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	session, err := db.IngestTranscript(t.Context(), transcriptBatch(modTime, 2048))
	require.NoError(t, err)

	baseline := modTime.Add(-24 * time.Hour)
	pinLastActivity(t, db, session.ID, baseline)

	require.NoError(t, db.gorm.WithContext(t.Context()).Exec(
		`UPDATE captain_session_sources SET parser_version = parser_version + 1 WHERE session_id = ?`,
		session.ID).Error)

	got := lastActivityAt(t, db, session.ID)
	require.NotNil(t, got)
	assert.True(t, got.Equal(baseline), "last_activity_at = %v, want untouched %v", got, baseline)
}

// Re-ingest re-derives one file. It must never erase activity recorded by
// prompt runs, turns, or events that the file never contained.
func TestLastActivityAt_IngestIsMonotonic(t *testing.T) {
	db := openLastActivityTestDB(t)
	modTime := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)

	session, err := db.IngestTranscript(t.Context(), transcriptBatch(modTime, 2048))
	require.NoError(t, err)

	t.Run("first ingest sets the value when the column is NULL", func(t *testing.T) {
		got := lastActivityAt(t, db, session.ID)
		require.NotNil(t, got)
		assert.True(t, got.Equal(modTime), "last_activity_at = %v, want %v", got, modTime)
	})

	t.Run("re-ingest does not roll last_activity_at backwards", func(t *testing.T) {
		newer := modTime.Add(time.Hour)
		pinLastActivity(t, db, session.ID, newer)

		_, err := db.IngestTranscript(t.Context(), transcriptBatch(modTime, 4096))
		require.NoError(t, err)

		got := lastActivityAt(t, db, session.ID)
		require.NotNil(t, got)
		assert.True(t, got.Equal(newer), "last_activity_at = %v, want preserved %v", got, newer)
	})

	// Guard: the monotonic clamp must not degrade into a no-op.
	t.Run("ingest still advances when the transcript is genuinely newer", func(t *testing.T) {
		advanced := modTime.Add(2 * time.Hour)
		batch := transcriptBatch(advanced, 8192)

		_, err := db.IngestTranscript(t.Context(), batch)
		require.NoError(t, err)

		got := lastActivityAt(t, db, session.ID)
		require.NotNil(t, got)
		assert.True(t, got.Equal(advanced), "last_activity_at = %v, want advanced %v", got, advanced)
	})
}

func TestLastActivityAt_ChildActivityOnlyRewritesSessionWhenAdvancing(t *testing.T) {
	db := openLastActivityTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	baseline := now.Add(-time.Hour)
	session := seedMaintenanceSession(t, db, "0195c1de-4ab8-7000-8000-0000000ac003")
	pinLastActivity(t, db, session.ID, baseline)

	before := readSessionTupleVersion(t, db, session.ID)
	require.NoError(t, db.gorm.WithContext(t.Context()).Exec(
		`INSERT INTO captain_events (session_id, kind, occurred_at) VALUES (?, ?, ?)`,
		session.ID, "test.older", baseline.Add(-time.Minute)).Error)
	afterOlderEvent := readSessionTupleVersion(t, db, session.ID)
	assert.Equal(t, before, afterOlderEvent, "older child activity must not create a dead captain_sessions tuple")

	advanced := baseline.Add(time.Minute)
	require.NoError(t, db.gorm.WithContext(t.Context()).Exec(
		`INSERT INTO captain_events (session_id, kind, occurred_at) VALUES (?, ?, ?)`,
		session.ID, "test.newer", advanced).Error)
	afterNewerEvent := readSessionTupleVersion(t, db, session.ID)
	assert.NotEqual(t, afterOlderEvent.Xmin, afterNewerEvent.Xmin, "newer child activity must update the session")
	assert.True(t, afterNewerEvent.LastActivityAt.Equal(advanced),
		"last_activity_at = %v, want %v", afterNewerEvent.LastActivityAt, advanced)
}
