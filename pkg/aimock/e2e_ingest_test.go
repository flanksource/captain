// ABOUTME: Ingests a transcript a real `claude` binary wrote while talking to the mock.
// ABOUTME: Closes the loop every fixture-string ingest spec leaves open.

package aimock_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/ai/provider"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/monitor"
)

// openIngestTestDB mirrors the gating the monitor suite uses: these specs need a
// real migrated schema, and a machine without the embedded-postgres binaries
// should skip rather than fail.
func openIngestTestDB(t *testing.T) *database.DB {
	t.Helper()
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run the embedded-postgres ingest e2e")
	}
	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_aimock",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })
	db, err := database.Open(t.Context(), database.WithDSN(dsn), database.WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

// TestE2EClaudeCLIIngest is the one ingest spec whose input nobody authored.
// Every other one feeds the parser a transcript captain's own developers typed,
// which proves only that the parser agrees with their idea of the format. Here a
// real claude binary writes the file and the assertion is what comes out the far
// end of discovery: a captain_sessions row carrying the run's token counts, and
// the transcript itself as messages.
func TestE2EClaudeCLIIngest(t *testing.T) {
	requireBinary(t, "claude")
	// Before HOME moves: the embedded-postgres binaries cache under the real one.
	db := openIngestTestDB(t)

	// HOME governs both halves of the pipeline — claude writes its transcript
	// beneath it, and the monitor discovers transcripts beneath the same root —
	// so pointing it at a temp dir makes this run the only session in the scan.
	home := t.TempDir()
	t.Setenv("HOME", home)
	cwd := filepath.Join(home, "work")
	require.NoError(t, os.MkdirAll(cwd, 0o755))

	srv := startAnthropic(t, "text-only.yaml")
	spec := hermetic(capitalPrompt, srv.Env())
	spec.SetCwd(cwd)

	events, err := provider.NewClaudeCLI("sonnet").ExecuteStream(context.Background(), spec)
	require.NoError(t, err)
	text, result := drain(t, events)
	require.Equal(t, capitalAnswer, text)
	require.NotNil(t, result, "the run must end with a result event")
	require.NotEmpty(t, result.SessionID)

	require.NoError(t, monitor.RunOnce(t.Context(), monitor.Config{
		DB:                db,
		HostID:            "aimock-e2e",
		DiscoverProcesses: func() ([]monitor.Process, error) { return nil, nil },
	}))

	overview, err := db.GetSessionOverviewByIdentity(t.Context(), result.SessionID)
	require.NoError(t, err, "the run must have produced a captain_sessions row")
	assert.Equal(t, "claude", overview.Source)
	require.NotNil(t, overview.CWD)
	assert.Equal(t, cwd, *overview.CWD)
	require.NotNil(t, overview.HistoryFile)
	assert.FileExists(t, *overview.HistoryFile)

	// The scenario's own numbers, having survived the binary, the transcript and
	// the parser — the accounting this pipeline exists to produce.
	assert.EqualValues(t, 14, overview.InputTokens)
	assert.EqualValues(t, 8, overview.OutputTokens)

	messages, err := db.ListTranscriptMessages(t.Context(), database.TranscriptPage{SessionID: overview.ID})
	require.NoError(t, err)
	require.NotEmpty(t, messages, "the transcript must land as messages, not just a session row")
	assert.EqualValues(t, len(messages), overview.MessageCount)
	assert.Contains(t, string(messages[len(messages)-1].Parts), capitalAnswer)
}
