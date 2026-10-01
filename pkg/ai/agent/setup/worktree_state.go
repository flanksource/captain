package setup

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/exec"
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
	out, err := gitRaw(dir, args...)
	return strings.TrimSpace(out), err
}

// gitRaw is git without the trim, for output whose leading whitespace or NUL
// separators carry meaning.
func gitRaw(dir string, args ...string) (string, error) {
	res := exec.NewExec("git", args...).WithCwd(dir).Run().Result()
	if res.Error != nil || res.ExitCode != 0 {
		return "", fmt.Errorf("setup: git %s in %s: exit %d: %v: %s",
			strings.Join(args, " "), dir, res.ExitCode, res.Error, strings.TrimSpace(res.Stderr))
	}
	return res.Stdout, nil
}

// recordWorktree reads the state of a freshly prepared worktree: its branch, the
// repository it was added to, and the commit it was branched from. With
// snapshot set, work-in-progress the setup copied in is committed as a setup
// snapshot, so the agent starts from a clean tree and its own work is exactly
// Setup..Head.
//
// Base is read from the worktree itself: `git worktree add` leaves HEAD on the
// start point it resolved — the configured base, the checkout ref, or the
// source's HEAD — and the copy of uncommitted work commits nothing.
func recordWorktree(path string, snapshot bool) (*api.WorktreeState, error) {
	branch, repo, err := inspectWorktree(path)
	if err != nil {
		return nil, err
	}
	base, err := git(path, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	state := &api.WorktreeState{Repo: repo, Path: path, Branch: branch, Base: base, Setup: base}
	if !snapshot {
		return state, nil
	}
	dirty, err := statusPaths(path)
	if err != nil {
		return nil, err
	}
	if len(dirty) == 0 {
		return state, nil
	}
	// `add -A` honours .gitignore, so ignored content the setup copied in stays
	// on disk and out of the snapshot.
	if _, err := git(path, "add", "-A"); err != nil {
		return nil, err
	}
	if _, err := git(path, "commit", "--no-verify", "-m", setupSnapshotMessage, "-m", setupSnapshotTrailer); err != nil {
		return nil, err
	}
	if state.Setup, err = git(path, "rev-parse", "HEAD"); err != nil {
		return nil, err
	}
	return state, nil
}

// inspectWorktree reads the branch a worktree has checked out and the repository
// it was added from. The source is git's main worktree — the first entry of
// `git worktree list` — rather than the checkout's path, which a remote checkout
// does not have.
func inspectWorktree(dir string) (branch, repo string, err error) {
	if branch, err = git(dir, "symbolic-ref", "--short", "HEAD"); err != nil {
		return "", "", err
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

// statusPaths lists the paths git reports as uncommitted in dir — modified,
// staged, deleted and untracked, but never ignored.
func statusPaths(dir string) ([]string, error) {
	out, err := gitRaw(dir, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	records := strings.Split(out, "\x00")
	for i := 0; i < len(records); i++ {
		record := records[i]
		if record == "" {
			continue
		}
		if len(record) < 4 {
			return nil, fmt.Errorf("setup: git status in %s: malformed record %q", dir, record)
		}
		paths = append(paths, record[3:])
		// A rename or copy is followed by its source path as a record of its own.
		if record[0] == 'R' || record[0] == 'C' {
			i++
		}
	}
	return paths, nil
}

// teardownWorktree decides the worktree's fate from its real git state. A dirty
// tree is kept — commons-db's cleanup force-removes, which would discard the
// agent's uncommitted edits — and so is one the run was told to keep (retain).
// A clean tree is removed, and its branch deleted when it holds nothing past
// Setup: the setup snapshot alone is not work worth a branch.
func teardownWorktree(wt *api.WorktreeState, retain string, cleanup func() error) error {
	head, err := git(wt.Path, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	dirty, err := statusPaths(wt.Path)
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
