package setup

import (
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/exec"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/workspace"
)

// setupSnapshotMessage marks the commit that carries work-in-progress copied
// from the source checkout, so nothing downstream mistakes it for the agent's.
const setupSnapshotMessage = "chore(setup): snapshot uncommitted changes"

const setupSnapshotTrailer = "Captain-Setup: true"

// Why teardown leaves a worktree in place.
const (
	keptKeepRequested = "keep requested"
	keptExisting      = "existing worktree"
	keptUncommitted   = "uncommitted changes"
)

// git runs `git <args...>` in dir and returns its trimmed stdout, failing loud
// with the command, directory and stderr on any non-zero exit.
func git(dir string, args ...string) (string, error) {
	res := exec.NewExec("git", args...).WithCwd(dir).Run().Result()
	if res.Error != nil || res.ExitCode != 0 {
		return "", fmt.Errorf("setup: git %s in %s: exit %d: %v: %s",
			strings.Join(args, " "), dir, res.ExitCode, res.Error, strings.TrimSpace(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout), nil
}

// recordWorktree reads the state of a prepared worktree: its branch, the
// repository it was added to, and the commit it was branched from.
//
// Base is read from the worktree itself: `git worktree add` leaves HEAD on the
// start point it resolved — the configured base, the checkout ref, or the
// source's HEAD — and the copy of uncommitted work commits nothing.
func recordWorktree(path string) (*api.WorktreeState, error) {
	branch, repo, err := inspectWorktree(path)
	if err != nil {
		return nil, err
	}
	base, err := git(path, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	return &api.WorktreeState{Repo: repo, Path: path, Branch: branch, Base: base, Setup: base}, nil
}

// snapshotSetup commits the work-in-progress the setup copied into a new
// worktree as a setup snapshot, so the agent starts from a clean tree and its
// own work is exactly Setup..Head. It commits through the workspace, so content
// cloned from the source's gitignored files never enters the snapshot even when
// the worktree's branch does not ignore it, and the run's configured commit
// gates apply. Nothing to commit leaves Setup at Base.
func snapshotSetup(ctx dbcontext.Context, ws *workspace.Workspace, state *api.WorktreeState, gates []workspace.Gate) error {
	result, err := ws.Commit(ctx, workspace.CommitOptions{
		Message:  setupSnapshotMessage,
		Trailers: []string{setupSnapshotTrailer},
		Gates:    gates,
	})
	if errors.Is(err, workspace.ErrNothingStaged) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("setup: snapshot uncommitted changes in %s: %w", ws.Dir, err)
	}
	state.Setup = result.SHA
	return nil
}

// inspectWorktree reads the branch a worktree has checked out and the repository
// it was added from. The source is git's main worktree — the first entry of
// `git worktree list` — rather than the checkout's path, which a remote checkout
// does not have.
func inspectWorktree(dir string) (branch, repo string, err error) {
	if branch, err = git(dir, "rev-parse", "--abbrev-ref", "HEAD"); err != nil {
		return "", "", err
	}
	if branch == "HEAD" {
		branch = ""
	}
	list, err := git(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return "", "", err
	}
	first, _, _ := strings.Cut(list, "\n")
	repo, ok := strings.CutPrefix(first, "worktree ")
	if !ok || repo == "" {
		return "", "", fmt.Errorf("setup: git worktree list in %s: unexpected first record %q", dir, first)
	}
	return branch, repo, nil
}

// teardownWorktree decides the worktree's fate from its real git state. A dirty
// tree is kept — commons-db's cleanup force-removes, which would discard the
// agent's uncommitted edits — and so is one the run was told to keep (retain).
// Dirty is the workspace's Changes: content cloned from the source's gitignored
// files is not the agent's work, so it never keeps a worktree.
// A clean tree is removed, and its branch deleted when it holds nothing past
// Setup: the setup snapshot alone is not work worth a branch — unless the run
// continued on a branch that already existed, which is never the run's to delete.
func teardownWorktree(ctx dbcontext.Context, ws *workspace.Workspace, wt *api.WorktreeState, moved *relocation, cleanup func() error) error {
	retain := moved.retainReason()
	head, err := git(wt.Path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	dirty, err := ws.Changes(ctx)
	if err != nil {
		return err
	}
	wt.Head, wt.Dirty = head, dirty
	if retain == "" && len(dirty) > 0 {
		wt.Kept, wt.KeptReason = true, keptUncommitted
		return nil
	}
	// A kept worktree's cleanup still runs: commons-db honours Keep itself, and
	// the same closure releases the setup's connections.
	if cleanup != nil {
		if err := cleanup(); err != nil {
			return fmt.Errorf("setup: tear down worktree %s: %w", wt.Path, err)
		}
	}
	if retain != "" {
		wt.Kept, wt.KeptReason = true, retain
		return nil
	}
	wt.Removed = true
	if moved.existingBranch {
		return nil
	}
	ahead, err := git(wt.Repo, "rev-list", "--count", wt.Setup+".."+wt.Head)
	if err != nil || ahead != "0" {
		return err
	}
	if _, err := git(wt.Repo, "branch", "-D", wt.Branch); err != nil {
		return err
	}
	wt.BranchDeleted = true
	return nil
}

// teardownNotice is the run's one line about what teardown did to the worktree.
func teardownNotice(wt *api.WorktreeState) string {
	switch {
	case wt.Kept:
		return fmt.Sprintf("[post-run] kept worktree %s on %s: %s", wt.Path, wt.Branch, wt.KeptReason)
	case wt.BranchDeleted:
		return fmt.Sprintf("[post-run] removed worktree %s; deleted branch %s", wt.Path, wt.Branch)
	default:
		return fmt.Sprintf("[post-run] removed worktree %s; branch %s @ %s", wt.Path, wt.Branch, shortSHA(wt.Head))
	}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
