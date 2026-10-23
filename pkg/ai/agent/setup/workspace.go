package setup

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/api"
)

// InspectWorkspace records an explicitly supplied directory and its linked worktree.
func InspectWorkspace(cwd string) (*api.WorkspaceRecord, error) {
	if _, err := os.Stat(cwd); err != nil {
		return nil, fmt.Errorf("inspect workspace %s: %w", cwd, err)
	}
	workspace := &api.WorkspaceRecord{Cwd: cwd}
	path, err := filepath.Abs(cwd)
	if err != nil {
		return nil, err
	}
	for {
		marker, err := os.Stat(filepath.Join(path, ".git"))
		if err == nil {
			if marker.IsDir() {
				return workspace, nil
			}
			gitDir, err := git(path, "rev-parse", "--absolute-git-dir")
			if err != nil {
				return nil, err
			}
			commonDir, err := git(path, "rev-parse", "--path-format=absolute", "--git-common-dir")
			if err != nil {
				return nil, err
			}
			if gitDir == commonDir {
				return workspace, nil
			}
			workspace.Worktree, err = recordWorktree(path, false)
			return workspace, err
		}
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect workspace %s: %w", cwd, err)
		}
		parent := filepath.Dir(path)
		if parent == path {
			return workspace, nil
		}
		path = parent
	}
}
