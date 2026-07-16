package database

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openMaintenanceTestDB(t *testing.T) *DB {
	t.Helper()
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres store tests")
	}
	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_maintenance_stores",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	db, err := Open(t.Context(), WithDSN(dsn), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func seedMaintenanceSession(t *testing.T, db *DB, providerSessionID string) *Session {
	t.Helper()
	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{
		ProviderSessionID: providerSessionID,
		Source:            "claude",
		HostID:            "test-host",
		CWD:               "/home/dev/example",
	})
	require.NoError(t, err)
	return session
}

func seedProcess(t *testing.T, db *DB, sessionID uuid.UUID, pid int64, sampledAt time.Time) {
	t.Helper()
	require.NoError(t, db.UpsertSessionProcess(t.Context(), SessionProcessInput{
		SessionID:        sessionID,
		HostID:           "test-host",
		BootID:           "boot-1",
		PID:              pid,
		ProcessStartedAt: sampledAt.Add(-time.Minute),
		Source:           "claude",
		SampledAt:        sampledAt,
	}))
}

func openProcessCount(t *testing.T, db *DB, sessionID uuid.UUID) int64 {
	t.Helper()
	var count int64
	require.NoError(t, db.gorm.WithContext(t.Context()).Model(&sessionProcessRecord{}).
		Where("session_id = ? AND ended_at IS NULL", sessionID).Count(&count).Error)
	return count
}

func TestSessionMaintenanceStore(t *testing.T) {
	db := openMaintenanceTestDB(t)
	now := time.Now().UTC().Truncate(time.Second)

	t.Run("EndSessionProcesses closes only the target session", func(t *testing.T) {
		target := seedMaintenanceSession(t, db, "0195c1de-4ab8-7000-8000-00000000e0d1")
		other := seedMaintenanceSession(t, db, "0195c1de-4ab8-7000-8000-00000000e0d2")
		seedProcess(t, db, target.ID, 5001, now)
		seedProcess(t, db, other.ID, 5002, now)

		closed, err := db.EndSessionProcesses(t.Context(), target.ID)
		require.NoError(t, err)
		assert.EqualValues(t, 1, closed)
		assert.EqualValues(t, 0, openProcessCount(t, db, target.ID))
		assert.EqualValues(t, 1, openProcessCount(t, db, other.ID))
	})

	t.Run("EndSessionProcesses requires a session id", func(t *testing.T) {
		_, err := db.EndSessionProcesses(t.Context(), uuid.Nil)
		require.ErrorIs(t, err, ErrInvalidSessionProcess)
	})

	t.Run("EndStaleSessionProcesses closes rows older than cutoff on any host", func(t *testing.T) {
		stale := seedMaintenanceSession(t, db, "0195c1de-4ab8-7000-8000-00000000e0d3")
		fresh := seedMaintenanceSession(t, db, "0195c1de-4ab8-7000-8000-00000000e0d4")
		seedProcess(t, db, stale.ID, 5003, now.Add(-2*time.Hour))
		seedProcess(t, db, fresh.ID, 5004, now)

		closed, err := db.EndStaleSessionProcesses(t.Context(), now.Add(-time.Hour))
		require.NoError(t, err)
		assert.EqualValues(t, 1, closed)
		assert.EqualValues(t, 0, openProcessCount(t, db, stale.ID))
		assert.EqualValues(t, 1, openProcessCount(t, db, fresh.ID))
	})

	t.Run("DeleteSessionSourcesByPaths prunes bookkeeping but keeps the session", func(t *testing.T) {
		modTime := now.Add(-time.Hour)
		session, err := db.IngestTranscript(t.Context(), testIngestBatch(modTime, 2048))
		require.NoError(t, err)

		deleted, err := db.DeleteSessionSourcesByPaths(t.Context(), []string{testTranscriptPath})
		require.NoError(t, err)
		assert.EqualValues(t, 1, deleted)

		sources, err := db.ListSessionSources(t.Context())
		require.NoError(t, err)
		assert.NotContains(t, sources, testTranscriptPath)

		kept, err := db.GetSession(t.Context(), session.ID)
		require.NoError(t, err)
		assert.Equal(t, session.ID, kept.ID)
	})

	t.Run("DeleteSessionSourcesByPaths with no paths is a no-op", func(t *testing.T) {
		deleted, err := db.DeleteSessionSourcesByPaths(t.Context(), nil)
		require.NoError(t, err)
		assert.EqualValues(t, 0, deleted)
	})

	t.Run("VacuumAnalyze succeeds", func(t *testing.T) {
		require.NoError(t, db.VacuumAnalyze(t.Context()))
	})

	t.Run("SessionStorageStats measures live tuple page occupancy", func(t *testing.T) {
		seedMaintenanceSession(t, db, "0195c1de-4ab8-7000-8000-00000000e0d5")
		var expectedLiveRows int64
		require.NoError(t, db.gorm.WithContext(t.Context()).Table("captain_sessions").Count(&expectedLiveRows).Error)

		stats, err := db.SessionStorageStats(t.Context())
		require.NoError(t, err)
		assert.Equal(t, expectedLiveRows, stats.LiveRows)
		assert.Positive(t, stats.HeapBytes)
		assert.Positive(t, stats.HeapPages)
		assert.Positive(t, stats.PagesWithLiveRows)
		assert.LessOrEqual(t, stats.PagesWithLiveRows, stats.HeapPages)
		assert.Positive(t, stats.LiveTupleBytes)
	})
}
