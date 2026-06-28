package git

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/clicky/exec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// initRepo creates a git repo with one commit in a temp dir and returns its path.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
	} {
		res := exec.NewExec("git", args...).WithCwd(dir).Run().Result()
		require.Equal(t, 0, res.ExitCode, "git %v: %s", args, res.Stderr)
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644))
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "seed"}} {
		res := exec.NewExec("git", args...).WithCwd(dir).Run().Result()
		require.Equal(t, 0, res.ExitCode, "git %v: %s", args, res.Stderr)
	}
	return dir
}

func TestWorktreeLifecycle(t *testing.T) {
	repo := initRepo(t)

	path, err := WorktreeAdd(repo, "captain/test", "")
	require.NoError(t, err)
	require.DirExists(t, path)

	// Edit a tracked file and add an untracked one inside the worktree.
	require.NoError(t, os.WriteFile(filepath.Join(path, "seed.txt"), []byte("seed\nmore\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(path, "new.txt"), []byte("new\n"), 0o644))

	changed, err := ChangedFiles(path)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"seed.txt", "new.txt"}, changed)

	diff, err := Diff(path)
	require.NoError(t, err)
	assert.Contains(t, diff, "seed.txt", "tracked change should appear in diff")

	sha, err := Commit(path, "captain: changes")
	require.NoError(t, err)
	assert.NotEmpty(t, sha)

	// After commit the tree is clean and a second commit is a no-op.
	noop, err := Commit(path, "captain: nothing")
	require.NoError(t, err)
	assert.Empty(t, noop, "clean tree ⇒ empty sha, no error")

	require.NoError(t, WorktreeRemove(repo, path))
	assert.NoDirExists(t, path)
}
