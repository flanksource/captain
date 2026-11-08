package monitor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsEphemeralClaudeTranscript(t *testing.T) {
	projectsDir := filepath.Join(t.TempDir(), ".claude", "projects")
	tempProject := claude.NormalizePath(filepath.Join(os.TempDir(), "TestCmuxExecutorFreshSessionReferencesPriorInPrompt123", "001"))

	assert.True(t, isEphemeralClaudeTranscript(projectsDir,
		filepath.Join(projectsDir, tempProject, "session.jsonl")))
	assert.True(t, isEphemeralClaudeTranscript(projectsDir,
		filepath.Join(projectsDir, claude.NormalizePath("/private/tmp/test-project"), "session", "subagents", "agent-a.jsonl")))
	assert.False(t, isEphemeralClaudeTranscript(projectsDir,
		filepath.Join(projectsDir, claude.NormalizePath("/Users/dev/src/project"), "session.jsonl")))
	assert.False(t, isEphemeralClaudeTranscript(projectsDir,
		filepath.Join(t.TempDir(), "outside-projects", "session.jsonl")))

	// A temp-rooted project whose working directory still exists is a live
	// session (e.g. an integration test that is currently running), not a stale
	// fixture, and must be kept.
	liveProject := filepath.Join(t.TempDir(), "work", "project")
	require.NoError(t, os.MkdirAll(liveProject, 0o755))
	assert.False(t, isEphemeralClaudeTranscript(projectsDir,
		filepath.Join(projectsDir, claude.NormalizePath(liveProject), "session.jsonl")),
		"a temp project whose working directory still exists must be backfilled")
}

func TestNormalizedDirectoryExists(t *testing.T) {
	tempRoot := filepath.Join(t.TempDir(), "_temp")
	liveProject := filepath.Join(tempRoot, "TestLiveSession", "001", "work", "project")
	require.NoError(t, os.MkdirAll(liveProject, 0o755))

	assert.True(t, normalizedDirectoryExists(tempRoot, claude.NormalizePath(liveProject)))
	assert.False(t, normalizedDirectoryExists(tempRoot,
		claude.NormalizePath(filepath.Join(tempRoot, "TestStaleSession", "001", "work", "project"))))
}

func TestDiscoverTranscriptsSkipsSystemTempClaudeProjects(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectsDir := filepath.Join(home, ".claude", "projects")
	normalProject := filepath.Join(projectsDir, claude.NormalizePath("/Users/dev/src/project"))
	tempProject := filepath.Join(projectsDir,
		claude.NormalizePath(filepath.Join(os.TempDir(), "TestCmuxExecutorFreshSessionReferencesPriorInPrompt123", "001")))

	normalRoot := filepath.Join(normalProject, "normal.jsonl")
	normalAgent := filepath.Join(normalProject, "normal", "subagents", "agent-a.jsonl")
	for _, path := range []string{
		normalRoot,
		normalAgent,
		filepath.Join(tempProject, "stale.jsonl"),
		filepath.Join(tempProject, "stale", "subagents", "agent-a.jsonl"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o644))
	}

	roots, agents := discoverTranscripts(time.Time{})
	require.Equal(t, []transcriptRef{{source: "claude", path: normalRoot}}, roots)
	require.Equal(t, []transcriptRef{{source: "claude", path: normalAgent}}, agents)
}

func TestDiscoverTranscriptsKeepsOnlyThoseModifiedSince(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	now := time.Now().Truncate(time.Second)
	lastWeek := now.Add(-7 * 24 * time.Hour)
	project := filepath.Join(home, ".claude", "projects", claude.NormalizePath("/Users/dev/src/project"))
	codexDay := filepath.Join(home, ".codex", "sessions", "2026", "07", "13")
	written := map[string]time.Time{
		filepath.Join(project, "old.jsonl"):                         lastWeek,
		filepath.Join(project, "new.jsonl"):                         now,
		filepath.Join(project, "old", "subagents", "agent-a.jsonl"): lastWeek,
		filepath.Join(project, "new", "subagents", "agent-b.jsonl"): now,
		filepath.Join(codexDay, "rollout-old.jsonl"):                lastWeek,
		filepath.Join(codexDay, "rollout-resumed.jsonl"):            now,
	}
	for path, modTime := range written {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o644))
		require.NoError(t, os.Chtimes(path, modTime, modTime))
	}

	roots, agents := discoverTranscripts(now.Add(-time.Hour))

	assert.ElementsMatch(t, []transcriptRef{
		{source: "claude", path: filepath.Join(project, "new.jsonl")},
		{source: "codex", path: filepath.Join(codexDay, "rollout-resumed.jsonl")},
	}, roots)
	assert.Equal(t, []transcriptRef{{source: "claude", path: filepath.Join(project, "new", "subagents", "agent-b.jsonl")}}, agents)
}

func TestBackfillSince(t *testing.T) {
	newest := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	older := newest.Add(-3 * time.Hour)
	current := func(modTime *time.Time) database.SessionSourceState {
		return database.SessionSourceState{ParserVersion: parserVersion, ObservedModTime: modTime}
	}
	for name, tc := range map[string]struct {
		sources map[string]database.SessionSourceState
		want    time.Time
	}{
		"nothing ingested scans everything": {nil, time.Time{}},
		"a day before the newest ingested transcript": {
			map[string]database.SessionSourceState{"a": current(&older), "b": current(&newest), "c": current(nil)},
			newest.Add(-backfillWatermarkLag),
		},
		"an older parser version scans everything": {
			map[string]database.SessionSourceState{
				"a": current(&newest),
				"b": {ParserVersion: parserVersion - 1, ObservedModTime: &older},
			},
			time.Time{},
		},
		"no recorded mtimes scans everything": {
			map[string]database.SessionSourceState{"a": current(nil)},
			time.Time{},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, backfillSince(tc.sources))
		})
	}
}

func TestDiscoverTranscriptsSkipsCodexAutoReviewSessions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	sessionsDir := filepath.Join(home, ".codex", "sessions", "2026", "07", "13")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))

	userSession := filepath.Join(sessionsDir, "user.jsonl")
	reviewSession := filepath.Join(sessionsDir, "review.jsonl")
	require.NoError(t, os.WriteFile(userSession, []byte(
		"{\"type\":\"session_meta\",\"payload\":{\"id\":\"user-1\",\"cwd\":\"/repo\"}}\n"+
			"{\"type\":\"turn_context\",\"payload\":{\"model\":\"gpt-5.6-sol\",\"effort\":\"high\"}}\n"), 0o644))
	require.NoError(t, os.WriteFile(reviewSession, []byte(
		"{\"type\":\"session_meta\",\"payload\":{\"id\":\"review-1\",\"cwd\":\"/repo\"}}\n"+
			"{\"type\":\"turn_context\",\"payload\":{\"model\":\"codex-auto-review\",\"effort\":\"low\"}}\n"), 0o644))

	roots, agents := discoverTranscripts(time.Time{})
	assert.Contains(t, roots, transcriptRef{source: "codex", path: userSession})
	assert.NotContains(t, roots, transcriptRef{source: "codex", path: reviewSession})
	assert.Empty(t, agents)
}
