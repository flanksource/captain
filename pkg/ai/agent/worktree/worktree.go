// Package worktree provides an agent.SetupPlugin that runs an AI agent inside a
// throwaway git worktree: it creates the worktree before the loop (pointing the
// run's Cwd at it) and, on teardown, captures the diff, optionally commits, and
// removes the worktree.
package worktree

import (
	"fmt"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/git"
)

// Plugin isolates an agent run in a git worktree.
type Plugin struct {
	Repo       string // repo root; defaults to RunContext.Repo when empty
	Branch     string // new branch name (required)
	Base       string // base ref; HEAD when empty
	CommitMsg  string // when set, the GENERIC commit path: commit all changes on teardown
	KeepOnExit bool   // keep the worktree + branch for inspection instead of removing

	Result *Result
}

// Result records what the run produced. Populated on teardown.
type Result struct {
	Path   string
	Branch string
	Diff   string
	Commit string
}

func (p *Plugin) Name() string { return "worktree" }

// Setup creates the worktree, points the run at it, and returns the teardown.
func (p *Plugin) Setup(rc *agent.RunContext) (func() error, error) {
	repo := p.Repo
	if repo == "" {
		repo = rc.Repo
	}
	if repo == "" {
		return nil, fmt.Errorf("worktree: Repo is required (set Plugin.Repo or Runner.Repo)")
	}
	if p.Branch == "" {
		return nil, fmt.Errorf("worktree: Branch is required")
	}

	path, err := git.WorktreeAdd(repo, p.Branch, p.Base)
	if err != nil {
		return nil, err
	}

	rc.Repo = repo
	rc.Cwd = path
	rc.Metadata["worktree"] = path
	if p.Result == nil {
		p.Result = &Result{}
	}
	p.Result.Path = path
	p.Result.Branch = p.Branch

	teardown := func() error {
		if diff, derr := git.Diff(path); derr == nil {
			p.Result.Diff = diff
		}
		if p.CommitMsg != "" {
			sha, cerr := git.Commit(path, p.CommitMsg)
			if cerr != nil {
				return cerr
			}
			p.Result.Commit = sha
		}
		if p.KeepOnExit {
			return nil
		}
		return git.WorktreeRemove(repo, path)
	}
	return teardown, nil
}
