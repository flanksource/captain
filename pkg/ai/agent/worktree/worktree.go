// Package worktree provides PreRun/PostRun hooks that isolate an agent run in a
// git worktree — a thin wrapper around the `wt` CLI (worktrunk.dev): PreRun
// switches to (creating) a worktree on a fresh branch, pointing the run's
// Workspace at it; PostRun optionally merges the branch back into Trunk and/or
// removes the worktree, gated by Merge/Cleanup and the run's outcome
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

	path string // resolved worktree path, set in PreRun
}

func (p *Plugin) Name() string { return "worktree" }

// switchResult is the shape of `wt switch --format=json`'s stdout.
type switchResult struct {
	Branch     string `json:"branch"`
	Path       string `json:"path"`
	BaseBranch string `json:"base_branch"`
}

// PreRun creates (or switches to) the worktree and points the run's Workspace
// at it, via `wt switch --create`.
func (p *Plugin) PreRun(hc *agent.HookContext) error {
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

	p.path = sw.Path
	ws.Repo = repo
	ws.Cwd = sw.Path
	ws.Branch = p.Branch
	ws.Base = sw.BaseBranch
	return nil
}

// mergeResult is the shape of `wt merge --format=json`'s stdout.
type mergeResult struct {
	Target string `json:"target"`
}

// PostRun merges the run's branch into Trunk and/or removes the worktree,
// gated by Merge/Cleanup and the run's outcome (hc.Failed / hc.Verified).
func (p *Plugin) PostRun(hc *agent.HookContext) error {
	if p.path == "" {
		return nil // PreRun never ran (or failed before creating a worktree)
	}
	ws := hc.Workspace()
	if diff, err := gitDiff(p.path); err == nil {
		ws.Diff = diff
	}

	merged := false
	if p.Merge.shouldMerge(hc.Failed, hc.Verified) {
		target, err := p.merge()
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
func (p *Plugin) merge() (string, error) {
	args := []string{"merge", "--no-remove", "--format=json"}
	if p.Trunk != "" {
		args = append(args, p.Trunk)
	}
	res := exec.NewExec("wt", args...).WithCwd(p.path).Run().Result()
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

// remove runs `wt remove` to delete the worktree (and, when merged/safe, its
// branch).
func (p *Plugin) remove(repo string) error {
	res := exec.NewExec("wt", "remove", p.Branch, "--format=json").WithCwd(repo).Run().Result()
	if res.Error != nil {
		return fmt.Errorf("wt remove %s: %w: %s", p.Branch, res.Error, strings.TrimSpace(res.Stderr))
	}
	return nil
}

// gitDiff is the one plain-git read this wrapper performs directly: `wt`
// exposes no command that returns the full uncommitted diff as text.
func gitDiff(dir string) (string, error) {
	res := exec.NewExec("git", "diff", "HEAD").WithCwd(dir).Run().Result()
	if res.Error != nil {
		return "", res.Error
	}
	return res.Stdout, nil
}
