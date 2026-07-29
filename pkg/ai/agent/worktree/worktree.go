// Package worktree provides PreRun/Post hooks that isolate an agent run in a
// git worktree — a thin wrapper around the `wt` CLI (worktrunk.dev): PreRun
// switches to (creating) a worktree on a fresh branch, pointing the run's
// Workspace at it; Post (at agent.PhaseRun) optionally merges the branch back
// into Trunk and/or removes the worktree, gated by Merge/Cleanup and the outcome
// (HookContext.Failed / HookContext.Verified). Captain implements no git logic
// itself here — `wt` does; captain only decides when to call it.
package worktree

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/exec"
)

// Plugin isolates an agent run in a git worktree managed by the `wt` CLI.
type Plugin struct {
	Repo   string // repo root; defaults to Workspace.Repo when empty
	Branch string // new branch name (required)
	Base   string // base ref for the new branch; wt's default branch when empty
	// Trunk is the `wt merge` target branch; wt's default branch when empty
	// (wt resolves this itself, which is more robust than hardcoding "main").
	Trunk string

	Merge   WorktreeMerge
	Cleanup WorktreeCleanup
}

func (p *Plugin) Name() string { return "worktree" }

// IsolatesWorkspace is always true: creating the worktree is what this hook is.
// It lets agent.EnsureSingleIsolator reject a run that also asks Spec.Setup for a
// checkout or worktree, rather than creating two trees and working in one.
func (p *Plugin) IsolatesWorkspace(*agent.HookContext) bool { return true }

// switchResult is the shape of `wt switch --format=json`'s stdout.
type switchResult struct {
	Branch     string `json:"branch"`
	Path       string `json:"path"`
	BaseBranch string `json:"base_branch"`
}

// PreRun creates (or switches to) the worktree and points the run's Workspace
// and request at it, via `wt switch --create`.
func (p *Plugin) PreRun(hc *agent.HookContext) error {
	if err := hc.EnsureSingleIsolator(); err != nil {
		return err
	}
	ws := hc.Workspace()
	repo := p.Repo
	if repo == "" {
		repo = ws.Repo
	}
	if repo == "" {
		return fmt.Errorf("worktree: Repo is required (set Plugin.Repo or Runner.Repo)")
	}
	if p.Branch == "" {
		return fmt.Errorf("worktree: Branch is required")
	}

	args := []string{"switch", "--create", p.Branch, "--no-cd", "--format=json"}
	if p.Base != "" {
		args = append(args, "--base", p.Base)
	}
	res := exec.NewExec("wt", args...).WithCwd(repo).Run().Result()
	if res.Error != nil {
		return fmt.Errorf("wt switch --create %s: %w: %s", p.Branch, res.Error, strings.TrimSpace(res.Stderr))
	}
	var sw switchResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &sw); err != nil {
		return fmt.Errorf("wt switch --create %s: parse JSON result: %w (stdout=%q)", p.Branch, err, res.Stdout)
	}

	ws.Repo = repo
	ws.Cwd = sw.Path
	ws.Branch = p.Branch
	ws.Base = sw.BaseBranch
	// The same transform the setup hook performs: the request said "isolate this
	// run", the tree now exists, so the spec stops describing the request and
	// starts describing where the work is. HookContext.Original keeps the former.
	hc.Request.SetCwd(sw.Path)
	return nil
}

// mergeResult is the shape of `wt merge --format=json`'s stdout.
type mergeResult struct {
	Target string `json:"target"`
}

// Phases declares the worktree teardown as the final phase, so any hook that
// commits at PhaseAgent still sees a live worktree.
func (p *Plugin) Phases() []agent.Phase { return []agent.Phase{agent.PhaseRun} }

// Post merges the run's branch into Trunk and/or removes the worktree, gated by
// Merge/Cleanup and the run's outcome (hc.Failed / hc.Verified).
func (p *Plugin) Post(hc *agent.HookContext, _ agent.Phase) error {
	ws := hc.Workspace()
	// PreRun records its effect on the workspace and nothing else writes Branch,
	// so a workspace not standing on p.Branch means no worktree was created —
	// PreRun never ran, or failed before `wt switch`.
	if p.Branch == "" || ws.Branch != p.Branch || ws.Cwd == "" {
		return nil
	}
	path := ws.Cwd
	if diff, err := gitDiff(path); err == nil {
		ws.Diff = diff
	}

	merged := false
	if p.Merge.shouldMerge(hc.Failed, hc.Verified) {
		target, err := p.merge(path)
		if err != nil {
			return err
		}
		merged = true
		p.recordMergeCommit(ws, target)
	}

	if p.Cleanup.shouldCleanup(merged, hc.Verified) {
		return p.remove(ws.Repo)
	}
	return nil
}

// merge runs `wt merge`, always passing --no-remove so Cleanup independently
// decides whether the worktree is removed. Returns the resolved target branch.
func (p *Plugin) merge(path string) (string, error) {
	args := []string{"merge", "--no-remove", "--format=json"}
	if p.Trunk != "" {
		args = append(args, p.Trunk)
	}
	res := exec.NewExec("wt", args...).WithCwd(path).Run().Result()
	if res.Error != nil {
		return "", fmt.Errorf("wt merge %s: %w: %s", p.Branch, res.Error, strings.TrimSpace(res.Stderr))
	}
	var mr mergeResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(res.Stdout)), &mr); err != nil {
		return "", fmt.Errorf("wt merge %s: parse JSON result: %w (stdout=%q)", p.Branch, err, res.Stdout)
	}
	return mr.Target, nil
}

// recordMergeCommit best-effort captures the merge commit's sha/message from
// the repo's target branch — a plain git read, since `wt merge`'s JSON result
// carries no commit metadata.
func (p *Plugin) recordMergeCommit(ws *api.Workspace, target string) {
	if target == "" || ws.Repo == "" {
		return
	}
	res := exec.NewExec("git", "log", "-1", target, "--format=%H%x00%s").WithCwd(ws.Repo).Run().Result()
	if res.Error != nil {
		return
	}
	sha, message, ok := strings.Cut(strings.TrimSpace(res.Stdout), "\x00")
	if !ok {
		return
	}
	ws.AddCommit(sha, message)
}

// remove runs `wt remove --force` to delete the worktree (and, when merged/safe,
// its branch). --force is required, not optional: a run that is not configured
// to commit always leaves the worktree dirty, and a plain `wt remove` refuses to
// delete a dirty worktree — which used to turn every such run into a teardown
// error. Cleanup policy, not git's dirty check, decides whether the tree goes.
func (p *Plugin) remove(repo string) error {
	res := exec.NewExec("wt", "remove", p.Branch, "--force", "--format=json").WithCwd(repo).Run().Result()
	if res.Error != nil {
		return fmt.Errorf("wt remove %s: %w: %s", p.Branch, res.Error, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// gitDiff is the one plain-git read this wrapper performs directly: `wt` exposes
// no command that returns the full uncommitted diff as text.
func gitDiff(dir string) (string, error) {
	res := exec.NewExec("git", "diff", "HEAD").WithCwd(dir).Run().Result()
	if res.Error != nil {
		return "", res.Error
	}
	untracked, err := untrackedDiff(dir)
	if err != nil {
		return "", err
	}
	return res.Stdout + untracked, nil
}

// untrackedDiff renders files git does not track yet as add-diffs. `git diff
// HEAD` omits them entirely, so a run whose only output was new files reported
// an empty diff. Deliberately read-only — `git add --intent-to-add` would show
// the same files but leaves them staged in the caller's index.
func untrackedDiff(dir string) (string, error) {
	res := exec.NewExec("git", "ls-files", "--others", "--exclude-standard", "-z").WithCwd(dir).Run().Result()
	if res.Error != nil {
		return "", fmt.Errorf("git ls-files --others: %w: %s", res.Error, strings.TrimSpace(res.Stderr))
	}
	var out strings.Builder
	for _, path := range strings.Split(res.Stdout, "\x00") {
		if path == "" {
			continue
		}
		// --no-index exits 1 whenever the two inputs differ, which is the normal
		// case here; only a code above 1 is a real failure.
		d := exec.NewExec("git", "diff", "--no-index", "--", "/dev/null", path).WithCwd(dir).Run().Result()
		if d.ExitCode > 1 {
			return "", fmt.Errorf("git diff --no-index %s: %s", path, strings.TrimSpace(d.Stderr))
		}
		out.WriteString(d.Stdout)
	}
	return out.String(), nil
}
