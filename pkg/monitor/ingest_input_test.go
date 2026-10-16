// Tests BuildIngestInput: a transcript stored outside the agents' own session
// roots ingests through IngestTranscript like one the monitor discovered.
package monitor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildIngestInputIngestsAClaudeTranscriptAtAnyPath(t *testing.T) {
	db := openMonitorTestDB(t)
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "uploads", "claude", "-workspace-src", fixtureSessionID+".jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(fixtureTranscript), 0o644))

	input, err := BuildIngestInput(t.Context(), db, "gavel-serve", "claude", path)
	require.NoError(t, err)
	_, err = db.IngestTranscript(t.Context(), input)
	require.NoError(t, err)

	overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
	require.NoError(t, err)
	assert.EqualValues(t, 2, overview.MessageCount)
	require.NotNil(t, overview.HistoryFile)
	assert.Equal(t, path, *overview.HistoryFile)

	t.Run("a re-uploaded, longer file ingests what was appended", func(t *testing.T) {
		require.NoError(t, os.WriteFile(path, []byte(fixtureTranscript+fixtureAppendLine), 0o644))
		input, err := BuildIngestInput(t.Context(), db, "gavel-serve", "claude", path)
		require.NoError(t, err)
		_, err = db.IngestTranscript(t.Context(), input)
		require.NoError(t, err)

		overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 3, overview.MessageCount)
	})

	t.Run("the monitor's recon leaves the unchanged registered file alone", func(t *testing.T) {
		noProcesses := func() ([]Process, error) { return nil, nil }
		require.NoError(t, RunOnce(t.Context(), Config{DB: db, HostID: "gavel-serve", DiscoverProcesses: noProcesses}))
		overview, err := db.GetSessionOverviewByIdentity(t.Context(), fixtureSessionID)
		require.NoError(t, err)
		assert.EqualValues(t, 3, overview.MessageCount)
	})
}

func TestBuildIngestInputFilesACodexSubagentUnderItsOwnThread(t *testing.T) {
	db := openMonitorTestDB(t)
	t.Setenv("HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "uploads", "codex")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	parent := filepath.Join(dir, "rollout-2026-08-20T09-00-00-parent-thread.jsonl")
	child := filepath.Join(dir, "rollout-2026-08-20T09-29-32-child-thread.jsonl")
	require.NoError(t, os.WriteFile(parent, []byte(codexParentRollout), 0o644))
	require.NoError(t, os.WriteFile(child, []byte(codexChildRollout), 0o644))

	for _, path := range []string{parent, child} {
		input, err := BuildIngestInput(t.Context(), db, "gavel-serve", "codex", path)
		require.NoError(t, err)
		_, err = db.IngestTranscript(t.Context(), input)
		require.NoError(t, err)
	}

	var rows []struct {
		ProviderSessionID string
		ParentProvider    *string
		Turns             int
	}
	require.NoError(t, db.Gorm().WithContext(t.Context()).Raw(`
		SELECT s.provider_session_id, p.provider_session_id AS parent_provider,
		       (SELECT count(*) FROM captain_turns t WHERE t.session_id = s.id) AS turns
		FROM captain_sessions s
		LEFT JOIN captain_sessions p ON p.id = s.parent_session_id
		WHERE s.provider_session_id IN (?, ?)
		ORDER BY s.provider_session_id`, "child-thread", "parent-thread").Scan(&rows).Error)
	require.Len(t, rows, 2, "each thread is its own session: %+v", rows)
	require.NotNil(t, rows[0].ParentProvider)
	assert.Equal(t, "parent-thread", *rows[0].ParentProvider)
	assert.Equal(t, 1, rows[0].Turns, "only the child's own turn, not the replayed parent turn")
	assert.Equal(t, 1, rows[1].Turns)
}

func TestBuildIngestInputRejectsAnUnknownSourceAndAMissingFile(t *testing.T) {
	db := openMonitorTestDB(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte(fixtureTranscript), 0o644))

	_, err := BuildIngestInput(t.Context(), db, "gavel-serve", "gemini", path)
	require.EqualError(t, err, `unknown transcript source "gemini" for `+path+`; want claude, codex or api`)

	missing := filepath.Join(t.TempDir(), "missing.jsonl")
	_, err = BuildIngestInput(t.Context(), db, "gavel-serve", "claude", missing)
	require.ErrorContains(t, err, "read claude transcript "+missing)
}
