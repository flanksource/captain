package cli

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons-db/shell"
	"github.com/google/uuid"
)

// resumeSetup is the setup a saved session continues in: its own directory, or
// the worktree that stood there, recreated from its branch when teardown removed
// it. The path stays the same because providers file a session under the
// directory it ran in.
func resumeSetup(ctx context.Context, item SessionGetItem, cwd string) (*shell.Setup, error) {
	unavailable := fmt.Sprintf("session working directory %s is unavailable", cwd)
	info, err := os.Stat(cwd)
	switch {
	case err == nil && info.IsDir():
		return &shell.Setup{Cwd: cwd}, nil
	case err == nil:
		return nil, newChatError(http.StatusUnprocessableEntity, unavailable+": not a directory")
	case !errors.Is(err, fs.ErrNotExist):
		return nil, newChatError(http.StatusUnprocessableEntity, fmt.Sprintf("%s: %v", unavailable, err))
	}
	worktree, err := recordedWorktree(ctx, item, cwd)
	if err != nil {
		return nil, err
	}
	if worktree == nil {
		return nil, newChatError(http.StatusUnprocessableEntity, unavailable)
	}
	return resumeWorktreeSetup(ctx, *worktree)
}

// recordedWorktree is the worktree the session's newest run in cwd recorded; nil
// when none did. A run may be filed on the session, or on the transcript row it
// executed in.
func recordedWorktree(ctx context.Context, item SessionGetItem, cwd string) (*api.WorktreeState, error) {
	ids := []string{item.CaptainID}
	if item.Execution != nil {
		ids = append(ids, item.Execution.CaptainID)
	}
	db, err := captainDB(ctx)
	if err != nil {
		return nil, err
	}
	var newest *database.PromptRun
	for _, raw := range ids {
		id, err := uuid.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("find the worktree of session %q: %w", raw, err)
		}
		for _, filter := range []database.PromptRunFilter{{SessionID: &id}, {ExecutionSessionID: &id}} {
			runs, err := db.ListPromptRuns(ctx, filter)
			if err != nil {
				return nil, fmt.Errorf("find the worktree of session %s: %w", id, err)
			}
			for i, run := range runs {
				if run.Workspace == nil || run.Workspace.Worktree == nil ||
					filepath.Clean(run.Workspace.Worktree.Path) != filepath.Clean(cwd) {
					continue
				}
				if newest == nil || run.QueuedAt.After(newest.QueuedAt) {
					newest = &runs[i]
				}
				break
			}
		}
	}
	if newest == nil {
		return nil, nil
	}
	return newest.Workspace.Worktree, nil
}

// resumeWorktreeSetup checks the worktree's branch out at its original path. It
// is kept when the chat ends: a chat commits nothing, and its teardown would
// force-remove the worktree along with the agent's edits.
func resumeWorktreeSetup(ctx context.Context, state api.WorktreeState) (*shell.Setup, error) {
	refuse := func(reason string) error {
		return newChatError(http.StatusUnprocessableEntity,
			fmt.Sprintf("session worktree %s was removed and cannot be recreated: %s", state.Path, reason))
	}
	switch {
	case state.Branch == "":
		return nil, refuse("it had no branch checked out")
	case state.BranchDeleted:
		return nil, refuse(fmt.Sprintf("branch %s was deleted with it", state.Branch))
	}
	if _, err := os.Stat(state.Repo); err != nil {
		return nil, refuse(fmt.Sprintf("its repository is unavailable: %v", err))
	}
	ref := "refs/heads/" + state.Branch
	command := exec.CommandContext(ctx, "git", "for-each-ref", "--format=%(refname)", ref)
	command.Dir = state.Repo
	out, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("look up branch %s in %s: %w: %s", state.Branch, state.Repo, err, strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) != ref {
		return nil, refuse(fmt.Sprintf("branch %s no longer exists in %s", state.Branch, state.Repo))
	}
	worktree := &shell.Worktree{Mode: shell.WorktreeBranch, Branch: state.Branch, Path: state.Path, Keep: true}
	worktree.ApplyDefaults()
	return &shell.Setup{Cwd: state.Repo, Checkout: &shell.Checkout{
		Mode: shell.CheckoutLocal, Path: state.Repo, Worktree: worktree,
	}}, nil
}
