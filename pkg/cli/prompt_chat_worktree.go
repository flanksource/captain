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
type resumeSetupOptions struct {
	CWD string
	// Recovery recreates a worktree whose branch is gone.
	Recovery WorktreeRecovery
}

func resumeSetup(ctx context.Context, item SessionGetItem, opts resumeSetupOptions) (*shell.Setup, error) {
	if err := validateWorktreeRecovery(opts.Recovery); err != nil {
		return nil, err
	}
	cwd := opts.CWD
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
	return resumeWorktreeSetup(ctx, *worktree, opts.Recovery)
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

// resumeWorktreeSetup checks the worktree's branch out at its original path. A
// branch that is gone is recreated by recovery, or the resume is refused with
// the recoveries it may choose. The worktree is kept when the chat ends: a chat
// commits nothing, and its teardown would force-remove the worktree along with
// the agent's edits.
func resumeWorktreeSetup(ctx context.Context, state api.WorktreeState, recovery WorktreeRecovery) (*shell.Setup, error) {
	refusal := fmt.Sprintf("session worktree %s was removed and cannot be recreated", state.Path)
	refuse := func(reason string) error {
		return newChatError(http.StatusUnprocessableEntity, refusal+": "+reason)
	}
	if err := validateWorktreeRecovery(recovery); err != nil {
		return nil, err
	}
	if state.Branch == "" {
		return nil, refuse("it had no branch checked out")
	}
	if _, err := os.Stat(state.Repo); err != nil {
		return nil, refuse(fmt.Sprintf("its repository is unavailable: %v", err))
	}
	ref := "refs/heads/" + state.Branch
	out, err := resumeGit(ctx, state.Repo, "for-each-ref", "--format=%(refname)", ref)
	if err != nil {
		return nil, err
	}
	if out != ref {
		children, err := resumeGit(ctx, state.Repo, "for-each-ref", "--count=1", "--format=%(refname)", ref+"/")
		if err != nil {
			return nil, err
		}
		if children != "" {
			return nil, refuse(fmt.Sprintf("branch %s cannot be recreated while %s exists", state.Branch, children))
		}
		gone := fmt.Sprintf("branch %s no longer exists in %s", state.Branch, state.Repo)
		if state.BranchDeleted {
			gone = fmt.Sprintf("branch %s was deleted with it", state.Branch)
		}
		if err := recoverWorktreeBranch(ctx, state, recovery, refusal+": "+gone); err != nil {
			return nil, err
		}
	}
	worktree := &shell.Worktree{Mode: shell.WorktreeBranch, Branch: state.Branch, Path: state.Path, Keep: true}
	worktree.ApplyDefaults()
	return &shell.Setup{Cwd: state.Repo, Checkout: &shell.Checkout{
		Mode: shell.CheckoutLocal, Path: state.Repo, Worktree: worktree,
	}}, nil
}

func validateWorktreeRecovery(recovery WorktreeRecovery) error {
	if recovery == "" || recovery == WorktreeRecoveryNew || recovery == WorktreeRecoveryHead {
		return nil
	}
	return newChatError(http.StatusBadRequest, fmt.Sprintf(
		"unknown worktree recovery %q, expected %q or %q", recovery, WorktreeRecoveryNew, WorktreeRecoveryHead))
}

// recoverWorktreeBranch recreates the worktree's missing branch the way
// recovery names: from the repository's HEAD, or at the commit the worktree
// last had. With no recovery it refuses with a 409 offering those that apply.
func recoverWorktreeBranch(ctx context.Context, state api.WorktreeState, recovery WorktreeRecovery, refusal string) error {
	headExists := state.Head != "" && commitExists(ctx, state.Repo, state.Head)
	switch {
	case recovery == WorktreeRecoveryNew:
		_, err := resumeGit(ctx, state.Repo, "branch", state.Branch, "HEAD")
		return err
	case recovery == WorktreeRecoveryHead && !headExists:
		return newChatError(http.StatusUnprocessableEntity,
			fmt.Sprintf("%s: its last commit %q no longer exists in %s", refusal, state.Head, state.Repo))
	case recovery == WorktreeRecoveryHead:
		_, err := resumeGit(ctx, state.Repo, "branch", state.Branch, state.Head)
		return err
	}
	offer := WorktreeUnavailable{
		Path: state.Path, Branch: state.Branch, Head: state.Head, Recoveries: []WorktreeRecovery{WorktreeRecoveryNew},
	}
	if headExists {
		offer.Recoveries = append(offer.Recoveries, WorktreeRecoveryHead)
	}
	return chatHTTPError{status: http.StatusConflict, msg: refusal, fields: map[string]any{"worktree": offer}}
}

// commitExists reports whether sha names a commit in repo; a branch deleted
// with `git branch -D` leaves its commits until they are garbage-collected.
func commitExists(ctx context.Context, repo, sha string) bool {
	return exec.CommandContext(ctx, "git", "-C", repo, "cat-file", "-e", sha+"^{commit}").Run() == nil
}

func resumeGit(ctx context.Context, repo string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = repo
	out, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s in %s: %w: %s", strings.Join(args, " "), repo, err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}
