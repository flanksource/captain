package monitor

import (
	"os"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHookEventsDriveIngest exercises the hooks-first flow with process
// discovery stubbed out entirely: session rows, ingest, and process teardown
// must all be driven by hook events alone.
func TestHookEventsDriveIngest(t *testing.T) {
	db := openMonitorTestDB(t)
	path := writeFixtureHome(t)

	m, err := New(Config{DB: db, HostID: "test-host",
		DiscoverProcesses: func() ([]Process, error) { return nil, nil }})
	require.NoError(t, err)
	ingestor := newIngestor(m)
	watcher, err := newTranscriptWatcher(m, ingestor)
	require.NoError(t, err)
	defer watcher.close()

	t.Run("SessionStart binds the session and arms tailing", func(t *testing.T) {
		m.handleHookEvent(t.Context(), watcher, ingestor, HookEvent{
			Provider: "claude", Event: "SessionStart", SessionID: fixtureSessionID,
			TranscriptPath: path, CWD: fixtureCWD, Detail: "startup", ReceivedAt: time.Now().UTC(),
		})
		overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 0, overview.MessageCount, "SessionStart must not ingest")
		assert.Equal(t, "claude", m.trackedPaths()[path])
	})

	t.Run("Stop ingests the transcript", func(t *testing.T) {
		m.handleHookEvent(t.Context(), watcher, ingestor, HookEvent{
			Provider: "claude", Event: "Stop", SessionID: fixtureSessionID,
			TranscriptPath: path, CWD: fixtureCWD, ReceivedAt: time.Now().UTC(),
		})
		overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 2, overview.MessageCount)
	})

	t.Run("transcript outside the provider root is rejected", func(t *testing.T) {
		m.handleHookEvent(t.Context(), watcher, ingestor, HookEvent{
			Provider: "claude", Event: "Stop", SessionID: fixtureSessionID,
			TranscriptPath: "/etc/passwd", ReceivedAt: time.Now().UTC(),
		})
		overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 2, overview.MessageCount, "rejected path must not ingest")
	})

	t.Run("SessionEnd closes the session's process rows and untracks", func(t *testing.T) {
		overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		require.NoError(t, db.UpsertSessionProcess(t.Context(), database.SessionProcessInput{
			SessionID: overview.ID, HostID: "test-host", BootID: "boot", PID: 4242,
			ProcessStartedAt: time.Now().UTC().Add(-time.Minute), Source: "claude",
		}))

		m.handleHookEvent(t.Context(), watcher, ingestor, HookEvent{
			Provider: "claude", Event: "SessionEnd", SessionID: fixtureSessionID,
			TranscriptPath: path, Detail: "prompt_input_exit", ReceivedAt: time.Now().UTC(),
		})

		overview, err = db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.False(t, overview.ProcessActive, "SessionEnd must close the process row")
		assert.NotContains(t, m.trackedPaths(), path)
	})

	t.Run("maintenance prunes bookkeeping for deleted transcripts", func(t *testing.T) {
		require.NoError(t, os.Remove(path))
		m.maintain(t.Context())
		sources, err := db.ListSessionSources(t.Context())
		require.NoError(t, err)
		assert.NotContains(t, sources, path)

		overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.NotEmpty(t, overview.ID, "the session itself must survive maintenance")
	})
}
