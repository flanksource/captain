// Package worktree provides PreRun/PostRun hooks that run an AI agent inside a
// throwaway git worktree: PreRun creates the worktree (pointing the run's
// Workspace at it) and PostRun captures the diff, optionally commits, and removes
// it. The outcome lands on the run's api.Workspace (Cwd/Branch/Diff/Commits).
package worktree

import (
	"fmt"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/git"
)

// Plugin isolates an agent run in a git worktree.
type Plugin struct {
	Repo       string // repo root; defaults to Workspace.Repo when empty
	Branch     string // new branch name (required)
	Base       string // base ref; HEAD when empty
	CommitMsg  string // when set, commit all changes in PostRun
	KeepOnExit bool   // keep the worktree + branch for inspection instead of removing

	path string // resolved worktree path, set in PreRun
}

func (p *Plugin) Name() string { return "worktree" }

// PreRun creates the worktree and points the run's Workspace at it.
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
	path, err := git.WorktreeAdd(repo, p.Branch, p.Base)
	if err != nil {
		return err
	}
	p.path = path
	ws.Repo = repo
	ws.Cwd = path
	ws.Branch = p.Branch
	ws.Base = p.Base
	return nil
}

// PostRun captures the diff, optionally commits, and removes the worktree.
func (p *Plugin) PostRun(hc *agent.HookContext) error {
	if p.path == "" {
		return nil
	}
	ws := hc.Workspace()
	if diff, err := git.Diff(p.path); err == nil {
		ws.Diff = diff
	}
	if p.CommitMsg != "" {
		sha, err := git.Commit(p.path, p.CommitMsg)
		if err != nil {
			return err
		}
		ws.AddCommit(sha, p.CommitMsg)
	}
	if p.KeepOnExit {
		return nil
	}
	return git.WorktreeRemove(ws.Repo, p.path)
}
