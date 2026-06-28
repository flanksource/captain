// Package git provides thin git helpers for captain's agent runs: creating and
// removing throwaway worktrees so an AI run can be isolated, capturing the diff
// it produced, committing it, and listing changed files. Everything shells out
// to git via clicky/exec; there is no go-git dependency.
package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/clicky/exec"
)

// run executes `git <args...>` in dir and returns trimmed stdout. A non-zero
// exit or exec error is surfaced (fail loud) with stderr context.
func run(dir string, args ...string) (string, error) {
	res := exec.NewExec("git", args...).WithCwd(dir).Run().Result()
	if res.Error != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), res.Error, strings.TrimSpace(res.Stderr))
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("git %s: exit %d: %s", strings.Join(args, " "), res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout), nil
}

// WorktreeAdd creates a new worktree for repo on a fresh branch (checked out
// from base, or HEAD when base is empty) and returns its filesystem path. The
// path lives under the OS temp dir and is unique per call; callers remove it via
// WorktreeRemove.
func WorktreeAdd(repo, branch, base string) (string, error) {
	if repo == "" || branch == "" {
		return "", fmt.Errorf("WorktreeAdd: repo and branch are required")
	}
	parent := filepath.Join(os.TempDir(), "captain-worktrees")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create worktree parent %s: %w", parent, err)
	}
	// git worktree add fails if the target path already exists, so make it
	// unique; a wall-clock suffix is fine for a throwaway directory name.
	path := filepath.Join(parent, fmt.Sprintf("%s-%d", sanitizeBranch(branch), time.Now().UnixNano()))

	args := []string{"worktree", "add", "-b", branch, path}
	if base != "" {
		args = append(args, base)
	}
	if _, err := run(repo, args...); err != nil {
		return "", err
	}
	return path, nil
}

// WorktreeRemove force-removes the worktree at path from repo (discarding any
// uncommitted changes there). It also prunes the now-dangling administrative
// entry so repeated runs stay clean.
func WorktreeRemove(repo, path string) error {
	if _, err := run(repo, "worktree", "remove", "--force", path); err != nil {
		return err
	}
	_, _ = run(repo, "worktree", "prune")
	return nil
}

// Diff returns the tracked changes in dir against HEAD (unified diff). Untracked
// files are not included; use ChangedFiles for the full set of touched paths.
func Diff(dir string) (string, error) {
	return run(dir, "diff", "HEAD")
}

// ChangedFiles returns the repo-relative paths modified in dir: tracked changes
// against HEAD plus untracked (non-ignored) files. This is the git-truth fallback
// for changed-file scoping when an agent mutated files via Bash rather than the
// Edit/Write tools.
func ChangedFiles(dir string) ([]string, error) {
	tracked, err := run(dir, "diff", "--name-only", "HEAD")
	if err != nil {
		return nil, err
	}
	untracked, err := run(dir, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	return uniqueNonEmptyLines(tracked, untracked), nil
}

// Commit stages everything in dir and commits it with msg, returning the new
// commit SHA. When there is nothing to commit it returns ("", nil) rather than
// failing — a clean tree after an AI run is a valid outcome, not an error.
func Commit(dir, msg string) (string, error) {
	if _, err := run(dir, "add", "-A"); err != nil {
		return "", err
	}
	status, err := run(dir, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	if status == "" {
		return "", nil
	}
	if _, err := run(dir, "commit", "-m", msg); err != nil {
		return "", err
	}
	return run(dir, "rev-parse", "HEAD")
}

// sanitizeBranch turns a branch name into a filesystem-safe directory segment.
func sanitizeBranch(branch string) string {
	return strings.NewReplacer("/", "-", " ", "-", string(filepath.Separator), "-").Replace(branch)
}

// uniqueNonEmptyLines splits each input on newlines and returns the distinct
// non-empty lines in first-seen order.
func uniqueNonEmptyLines(blocks ...string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, b := range blocks {
		for _, line := range strings.Split(b, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			if _, dup := seen[line]; dup {
				continue
			}
			seen[line] = struct{}{}
			out = append(out, line)
		}
	}
	return out
}
