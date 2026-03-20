package claude

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveWorktreeRoot(t *testing.T) {
	tmpDir := t.TempDir()

	mainRepo := filepath.Join(tmpDir, "main-repo")
	gitDir := filepath.Join(mainRepo, ".git", "worktrees", "my-branch")
	require.NoError(t, os.MkdirAll(gitDir, 0755))

	worktreeDir := filepath.Join(tmpDir, "worktree")
	require.NoError(t, os.MkdirAll(worktreeDir, 0755))

	gitFile := filepath.Join(worktreeDir, ".git")
	require.NoError(t, os.WriteFile(gitFile, []byte("gitdir: "+gitDir+"\n"), 0644))

	t.Run("resolves worktree to main repo root", func(t *testing.T) {
		root := resolveWorktreeRoot(gitFile)
		assert.Equal(t, mainRepo, root)
	})

	t.Run("returns empty for regular git dir", func(t *testing.T) {
		regularGit := filepath.Join(mainRepo, ".git")
		root := resolveWorktreeRoot(regularGit)
		assert.Empty(t, root)
	})

	t.Run("returns empty for non-existent file", func(t *testing.T) {
		root := resolveWorktreeRoot(filepath.Join(tmpDir, "nope"))
		assert.Empty(t, root)
	})
}

func TestFindProjectInfo_Worktree(t *testing.T) {
	tmpDir := t.TempDir()

	mainRepo := filepath.Join(tmpDir, "main-repo")
	gitDir := filepath.Join(mainRepo, ".git", "worktrees", "feat-branch")
	require.NoError(t, os.MkdirAll(gitDir, 0755))

	worktreeDir := filepath.Join(tmpDir, "wt")
	subDir := filepath.Join(worktreeDir, "pkg", "foo")
	require.NoError(t, os.MkdirAll(subDir, 0755))

	gitFile := filepath.Join(worktreeDir, ".git")
	require.NoError(t, os.WriteFile(gitFile, []byte("gitdir: "+gitDir+"\n"), 0644))

	t.Run("Root is worktree dir for relative paths", func(t *testing.T) {
		root := FindProjectRoot(subDir)
		assert.Equal(t, worktreeDir, root)
	})

	t.Run("MainRoot is main repo for project naming", func(t *testing.T) {
		info := FindProjectInfo(subDir)
		assert.Equal(t, worktreeDir, info.Root)
		assert.Equal(t, mainRepo, info.MainRoot)
		assert.Equal(t, ".git", info.MarkerFile)
	})
}
