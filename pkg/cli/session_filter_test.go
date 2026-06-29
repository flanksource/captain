package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSplitSessionIDPathArgs(t *testing.T) {
	sessionIDs, paths := splitSessionIDPathArgs([]string{
		"pkg/cli",
		"019e0365-dc2a-7ad0-a5a8-78936481a928",
		"README.md",
		"6522FE00-9A7C-4CEE-9A1A-123456789ABC",
	})

	assert.Equal(t, []string{
		"019e0365-dc2a-7ad0-a5a8-78936481a928",
		"6522fe00-9a7c-4cee-9a1a-123456789abc",
	}, sessionIDs)
	assert.Equal(t, []string{"pkg/cli", "README.md"}, paths)
}

func TestNormalizeHistoryOptionsTreatsUUIDArgsAsSessions(t *testing.T) {
	opts, sessionIDs, err := normalizeHistoryOptions(HistoryOptions{
		Paths:     []string{"019e0365-dc2a-7ad0-a5a8-78936481a928", "pkg/cli"},
		SessionID: "sess-prefix",
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"pkg/cli"}, opts.Paths)
	assert.Equal(t, []string{"sess-prefix", "019e0365-dc2a-7ad0-a5a8-78936481a928"}, sessionIDs)
}

func TestNormalizeSessionIDFiltersRejectsConflictingAliases(t *testing.T) {
	_, err := normalizeSessionIDFilters("old", "new", nil)
	require.Error(t, err)
}

func TestFilterCostsBySessionIDSupportsPrefixes(t *testing.T) {
	costs := []claude.SessionCost{
		{SessionID: "019e0365-dc2a-7ad0-a5a8-78936481a928"},
		{SessionID: "6522fe00-9a7c-4cee-9a1a-123456789abc"},
		{SessionID: "other"},
	}

	got := filterCostsBySessionID(costs, []string{"019e0365", "6522fe00-9a7c"})
	require.Len(t, got, 2)
	assert.Equal(t, "019e0365-dc2a-7ad0-a5a8-78936481a928", got[0].SessionID)
	assert.Equal(t, "6522fe00-9a7c-4cee-9a1a-123456789abc", got[1].SessionID)
}

func TestHistoryScanCWDUsesProjectPathArgs(t *testing.T) {
	defaultCWD := t.TempDir()
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/repo\n"), 0o644))
	subdir := filepath.Join(repo, "pkg", "cli")
	require.NoError(t, os.MkdirAll(subdir, 0o755))

	assert.Equal(t, repo, historyScanCWD(defaultCWD, []string{subdir}, false))
	assert.Equal(t, defaultCWD, historyScanCWD(defaultCWD, []string{subdir}, true))
}

func TestHistoryScanCWDLeavesConflictingProjectPathsAlone(t *testing.T) {
	defaultCWD := t.TempDir()
	repoA := t.TempDir()
	repoB := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repoA, "go.mod"), []byte("module example.com/a\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(repoB, "go.mod"), []byte("module example.com/b\n"), 0o644))

	assert.Equal(t, defaultCWD, historyScanCWD(defaultCWD, []string{repoA, repoB}, false))
}

func TestCollectCodexHistoryFiltersByMetadata(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(repo, "go.mod"), []byte("module example.com/repo\n"), 0o644))
	otherRepo := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(otherRepo, "go.mod"), []byte("module example.com/other\n"), 0o644))

	writeCodexHistoryFixture(t, home, "match", "sess-match", repo, "hello")
	writeCodexHistoryFixture(t, home, "other", "sess-other", otherRepo, "skip me")

	uses, err := collectCodexHistory(repo, false, claude.Filter{SessionID: "sess-match"})
	require.NoError(t, err)
	require.Len(t, uses, 1)
	assert.Equal(t, "sess-match", uses[0].SessionID)
	assert.Equal(t, "hello", uses[0].Input["text"])

	uses, err = collectCodexHistory(repo, false, claude.Filter{SessionID: "sess-other"})
	require.NoError(t, err)
	assert.Empty(t, uses)
}

func writeCodexHistoryFixture(t *testing.T, home, name, sessionID, cwd, message string) {
	t.Helper()
	dir := filepath.Join(home, ".codex", "sessions", "2026", "06", "29")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	content := fmt.Sprintf(
		`{"timestamp":"2026-06-29T10:00:00Z","type":"session_meta","payload":{"id":%q,"cwd":%q}}`+"\n"+
			`{"timestamp":"2026-06-29T10:00:01Z","type":"event_msg","payload":{"type":"agent_message","message":%q}}`+"\n",
		sessionID,
		cwd,
		message,
	)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".jsonl"), []byte(content), 0o644))
}
