package monitor

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
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
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_monitor"})
	db, err := database.Open(t.Context(), database.WithDSN(handle.DSN()), database.WithMigrations())
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
	fakeDiscover := func() ([]Process, error) {
		return []Process{{
			Source: "claude", PID: 4242, Status: "sleeping", CPUPercent: 20.5, MemoryPercent: 1.5,
			MemoryRSSKB: 1024, StartedAt: &processStart, CWD: fixtureCWD,
			Command: "claude --resume " + fixtureSessionID,
		}}, nil
	}

	require.NoError(t, RunOnce(t.Context(), Config{DB: db, HostID: "test-host", DiscoverProcesses: fakeDiscover}))

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
		require.NoError(t, RunOnce(t.Context(), Config{DB: db, HostID: "test-host", DiscoverProcesses: fakeDiscover}))
		overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 2, overview.MessageCount, "unchanged transcript must not re-ingest")

		// Transcripts are append-only and a live session is appended to
		// constantly, so what matters is not just the row count afterwards but
		// how many rows the pass offered the database. Without a high-water
		// mark every append re-submits the whole file and the conflict work
		// grows with transcript length rather than with what was written.
		attempted := 0
		require.NoError(t, db.Gorm().Callback().Create().Before("gorm:create").
			Register("test:count_message_writes", func(tx *gorm.DB) {
				if tx.Statement.Table == "captain_messages" && tx.Statement.ReflectValue.Kind() == reflect.Slice {
					attempted += tx.Statement.ReflectValue.Len()
				}
			}))
		t.Cleanup(func() {
			require.NoError(t, db.Gorm().Callback().Create().Remove("test:count_message_writes"))
		})

		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
		require.NoError(t, err)
		_, err = f.WriteString(fixtureAppendLine)
		require.NoError(t, err)
		require.NoError(t, f.Close())

		require.NoError(t, RunOnce(t.Context(), Config{DB: db, HostID: "test-host", DiscoverProcesses: fakeDiscover}))
		overview, err = db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 3, overview.MessageCount, "appended message must ingest incrementally")
		assert.Equal(t, 1, attempted, "only the appended line may be offered to the database")

		messages, err := db.ListTranscriptMessages(t.Context(), database.TranscriptPage{SessionID: overview.ID, Tail: 1})
		require.NoError(t, err)
		require.Len(t, messages, 1)
		require.NotNil(t, messages[0].SourceLine)
		assert.EqualValues(t, 3, *messages[0].SourceLine)
	})

	t.Run("process vanish closes the process row", func(t *testing.T) {
		noProcesses := func() ([]Process, error) { return nil, nil }
		require.NoError(t, RunOnce(t.Context(), Config{DB: db, HostID: "test-host", DiscoverProcesses: noProcesses}))
		overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.False(t, overview.ProcessActive)
	})

	t.Run("writer lock excludes a second monitor", func(t *testing.T) {
		m, err := New(Config{DB: db, HostID: "test-host"})
		require.NoError(t, err)
		conn, holderPID, err := m.tryAcquireWriterLock(t.Context())
		require.NoError(t, err)
		require.NotNil(t, conn)
		assert.Zero(t, holderPID)
		defer func() { require.NoError(t, conn.Close()) }()

		second, err := New(Config{DB: db, HostID: "test-host"})
		require.NoError(t, err)
		conn2, holderPID, err := second.tryAcquireWriterLock(t.Context())
		require.NoError(t, err)
		assert.Nil(t, conn2, "second monitor must not acquire the writer lock")
		assert.Equal(t, os.Getpid(), holderPID)
		if conn2 != nil {
			_ = conn2.Close()
		}
	})
}
