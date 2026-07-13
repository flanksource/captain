package monitor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/database"
	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	fixtureSessionID = "0195c1de-4ab8-7000-8000-0123456789ab"
	fixtureCWD       = "/home/dev/example"
)

const fixtureTranscript = `{"type":"user","uuid":"u-1","sessionId":"` + fixtureSessionID + `","timestamp":"2026-07-05T10:00:00Z","cwd":"` + fixtureCWD + `","message":{"role":"user","content":"hello"}}
{"type":"assistant","uuid":"a-1","sessionId":"` + fixtureSessionID + `","timestamp":"2026-07-05T10:00:05Z","message":{"role":"assistant","model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":20},"content":[{"type":"text","text":"hi there"}]}}
`

const fixtureAppendLine = `{"type":"assistant","uuid":"a-2","sessionId":"` + fixtureSessionID + `","timestamp":"2026-07-05T10:01:00Z","message":{"role":"assistant","model":"claude-sonnet-5","usage":{"input_tokens":150,"output_tokens":30},"content":[{"type":"text","text":"appended"}]}}
`

func openMonitorTestDB(t *testing.T) *database.DB {
	t.Helper()
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres monitor tests")
	}
	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_monitor",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })
	db, err := database.Open(t.Context(), database.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// writeFixtureHome points HOME at a temp dir holding one claude transcript so
// discovery, ingest, and re-ingest run against a controlled filesystem.
func writeFixtureHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "projects", "-home-dev-example")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, fixtureSessionID+".jsonl")
	require.NoError(t, os.WriteFile(path, []byte(fixtureTranscript), 0o644))
	return path
}

func TestRunOnceIngestsAndIsIncremental(t *testing.T) {
	db := openMonitorTestDB(t)
	path := writeFixtureHome(t)

	processStart := time.Now().Add(-time.Minute).Truncate(time.Second)
	discoverProcesses = func() ([]agentProcess, error) {
		return []agentProcess{{
			Source: "claude", PID: 4242, Status: "sleeping", CPUPercent: 20.5, MemoryPercent: 1.5,
			MemoryRSSKB: 1024, StartedAt: &processStart, CWD: fixtureCWD,
			Command: "claude --resume " + fixtureSessionID,
		}}, nil
	}
	t.Cleanup(func() { discoverProcesses = discoverAgentProcesses })

	require.NoError(t, RunOnce(t.Context(), Config{DB: db, HostID: "test-host"}))

	overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, overview.MessageCount)
	assert.EqualValues(t, 120, overview.InputTokens+overview.OutputTokens)
	assert.True(t, overview.ProcessActive, "live process must be recorded")
	require.NotNil(t, overview.CPUPercent)
	assert.InDelta(t, 20.5, *overview.CPUPercent, 1e-9)
	require.NotNil(t, overview.HistoryFile)
	assert.Equal(t, path, *overview.HistoryFile)

	t.Run("unchanged file is skipped, appended file re-ingests", func(t *testing.T) {
		require.NoError(t, RunOnce(t.Context(), Config{DB: db, HostID: "test-host"}))
		overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 2, overview.MessageCount, "unchanged transcript must not re-ingest")

		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		_, err = f.WriteString(fixtureAppendLine)
		require.NoError(t, err)
		require.NoError(t, f.Close())

		require.NoError(t, RunOnce(t.Context(), Config{DB: db, HostID: "test-host"}))
		overview, err = db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 3, overview.MessageCount, "appended message must ingest incrementally")

		messages, err := db.ListTranscriptMessages(t.Context(), database.TranscriptPage{SessionID: overview.ID, Tail: 1})
		require.NoError(t, err)
		require.Len(t, messages, 1)
		require.NotNil(t, messages[0].SourceLine)
		assert.EqualValues(t, 3, *messages[0].SourceLine)
	})

	t.Run("process vanish closes the process row", func(t *testing.T) {
		discoverProcesses = func() ([]agentProcess, error) { return nil, nil }
		require.NoError(t, RunOnce(t.Context(), Config{DB: db, HostID: "test-host"}))
		overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.False(t, overview.ProcessActive)
	})

	t.Run("writer lock excludes a second monitor", func(t *testing.T) {
		m, err := New(Config{DB: db, HostID: "test-host"})
		require.NoError(t, err)
		conn, acquired, err := m.tryAcquireWriterLock(t.Context())
		require.NoError(t, err)
		require.True(t, acquired)
		defer func() { require.NoError(t, conn.Close()) }()

		second, err := New(Config{DB: db, HostID: "test-host"})
		require.NoError(t, err)
		conn2, acquired2, err := second.tryAcquireWriterLock(t.Context())
		require.NoError(t, err)
		assert.False(t, acquired2, "second monitor must not acquire the writer lock")
		if conn2 != nil {
			_ = conn2.Close()
		}
	})
}
