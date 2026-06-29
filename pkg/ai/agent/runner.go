// Package agent composes pre/post lifecycle plugins around an iterative AI run.
//
// It sits one layer above ai.RunUntil: a Runner drives the loop while
// SetupPlugins (e.g. a git worktree) run once before it, VerifyPlugins (e.g.
// lint/test/LLM-judge) vote after every iteration and drive re-runs when they
// fail, and FinalizePlugins (e.g. a commit) run once after the loop. This is the
// extension point ai/middleware (a single-call decorator) cannot express.
//
// The Runner also taps the event stream to record the run's SessionID and the
// set of files the agent changed (reusing pkg/ai/history's file-mutating tool
// table), so verifiers and finalizers can scope themselves to just those files.
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
)

// Scope controls how much verifiers/finalizers act on. ScopeChanged restricts
// them to the files the agent edited; ScopeAll lets each act on the whole tree.
type Scope string

const (
	ScopeChanged Scope = "changed"
	ScopeAll     Scope = "all"
)

// AllScopes lists every verifier scope in canonical order. It is the single
// source of truth behind Scope.Valid, ScopeList, ParseScope, and the
// help/error/completion strings that enumerate scopes.
func AllScopes() []Scope {
	return []Scope{ScopeAll, ScopeChanged}
}

// Valid reports whether s is one of the supported scopes.
func (s Scope) Valid() bool {
	for _, x := range AllScopes() {
		if s == x {
			return true
		}
	}
	return false
}

// ScopeList renders the supported scopes as a comma-separated string for
// help/error text.
func ScopeList() string {
	parts := make([]string, len(AllScopes()))
	for i, s := range AllScopes() {
		parts[i] = string(s)
	}
	return strings.Join(parts, ", ")
}

// ParseScope resolves a CLI/flag value into a Scope, defaulting empty to
// ScopeAll. It fails loud on any other value, naming the valid set.
func ParseScope(s string) (Scope, error) {
	switch Scope(s) {
	case "", ScopeAll:
		return ScopeAll, nil
	case ScopeChanged:
		return ScopeChanged, nil
	default:
		return "", fmt.Errorf("invalid --scope %q (valid: %s)", s, ScopeList())
	}
}

// RunContext is the shared per-run state passed to every plugin. Setup plugins
// may rewrite Cwd (a worktree); the Runner fills SessionID/ChangedFiles from the
// event stream as the loop progresses.
type RunContext struct {
	Ctx   context.Context
	Repo  string // repo root (for git/commit/test scoping)
	Cwd   string // working dir the provider runs in; a worktree plugin rewrites it
	Scope Scope  // ScopeChanged ⇒ verifiers/finalizers act on ChangedFiles only

	SessionID    string   // last EventSystem.SessionID seen
	ChangedFiles []string // distinct repo-relative paths from file-mutating tool_use events

	Metadata map[string]any // plugin scratch (worktree path/branch/diff, …)
}

// Verdict is a verifier's judgement on an iteration. Feedback is appended to the
// next iteration's prompt when OK is false.
type Verdict struct {
	OK       bool
	Reason   string
	Feedback string
}

// Plugin is the common base; concrete plugins also implement one or more of the
// hook interfaces below.
type Plugin interface{ Name() string }

// SetupPlugin runs once before the loop and returns a teardown closure run after
// it (teardowns run in LIFO order). A worktree plugin creates the worktree here.
type SetupPlugin interface {
	Plugin
	Setup(rc *RunContext) (teardown func() error, err error)
}

// VerifyPlugin runs after each completed iteration and votes. A non-OK verdict
// triggers a re-run with the verdict's Feedback appended to the prompt.
type VerifyPlugin interface {
	Plugin
	Verify(rc *RunContext, iter *ai.LoopIteration) (Verdict, error)
}

// FinalizePlugin runs once after the loop ends cleanly, before teardowns — so a
// commit plugin can stage and commit while a worktree still exists.
type FinalizePlugin interface {
	Plugin
	Finalize(rc *RunContext, result *RunResult) error
}

// Runner composes plugins around ai.RunUntil.
type Runner struct {
	Provider ai.StreamingProvider
	Plugins  []Plugin
	Loop     ai.LoopOptions // MaxIterations, MaxCostUSD, SessionReuse, OnEvent honoured

	// Build assembles the per-iteration request. feedback is the aggregated
	// verifier feedback from the previous turn ("" on the first turn).
	Build func(rc *RunContext, iter int, prev *ai.LoopIteration, feedback string) ai.Request

	Repo  string // seeds RunContext.Repo
	Cwd   string // seeds RunContext.Cwd
	Scope Scope  // seeds RunContext.Scope (defaults to ScopeAll)
}

// RunResult bundles the outcome.
type RunResult struct {
	Loop         *ai.LoopResult
	Verdicts     []Verdict
	Cwd          string
	SessionID    string
	ChangedFiles []string
}

// Run executes the full pipeline: setup → loop (with per-iteration verify) →
// finalize → teardown. Errors are surfaced in priority order (loop, verify,
// finalize, teardown); teardowns always run.
func (r *Runner) Run(ctx context.Context) (*RunResult, error) {
	if r.Provider == nil {
		return nil, fmt.Errorf("agent.Runner: Provider is required")
	}
	if r.Build == nil {
		return nil, fmt.Errorf("agent.Runner: Build is required")
	}

	rc := &RunContext{Ctx: ctx, Repo: r.Repo, Cwd: r.Cwd, Scope: r.Scope, Metadata: map[string]any{}}
	if rc.Scope == "" {
		rc.Scope = ScopeAll
	}
	result := &RunResult{}

	// 1. Setup plugins (in order); collect teardowns to run LIFO at the end.
	var teardowns []func() error
	runTeardowns := func() error {
		var firstErr error
		for i := len(teardowns) - 1; i >= 0; i-- {
			if err := teardowns[i](); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
	for _, p := range r.Plugins {
		sp, ok := p.(SetupPlugin)
		if !ok {
			continue
		}
		td, err := sp.Setup(rc)
		if err != nil {
			_ = runTeardowns()
			return nil, fmt.Errorf("agent: setup plugin %q: %w", p.Name(), err)
		}
		if td != nil {
			teardowns = append(teardowns, td)
		}
	}

	// 2. Drive the loop. The event tap records session + changed files; the
	// BuildRequest hook runs verifiers between iterations.
	var verifyErr error
	var feedback string
	loop := r.Loop
	loop.Provider = r.Provider
	callerOnEvent := r.Loop.OnEvent
	loop.OnEvent = func(iter int, ev ai.Event) {
		r.recordEvent(rc, ev)
		if callerOnEvent != nil {
			callerOnEvent(iter, ev)
		}
	}
	loop.BuildRequest = func(iter int, prev *ai.LoopIteration) (ai.Request, bool) {
		if prev != nil {
			v, err := r.runVerifiers(rc, prev)
			if err != nil {
				verifyErr = err
				return ai.Request{}, false
			}
			result.Verdicts = append(result.Verdicts, v)
			if v.OK {
				return ai.Request{}, false // success → stop ("condition-met")
			}
			feedback = v.Feedback
		}
		req := r.Build(rc, iter, prev, feedback)
		req.Cwd = rc.Cwd
		return req, true
	}

	lr, loopErr := ai.RunUntil(ctx, loop)
	result.Loop = lr
	result.Cwd = rc.Cwd
	result.SessionID = rc.SessionID
	result.ChangedFiles = rc.ChangedFiles

	// 3. Finalize (only on a clean loop) — runs before teardown so a commit
	// lands while a worktree still exists.
	var finalizeErr error
	if loopErr == nil && verifyErr == nil {
		for _, p := range r.Plugins {
			fp, ok := p.(FinalizePlugin)
			if !ok {
				continue
			}
			if err := fp.Finalize(rc, result); err != nil {
				finalizeErr = fmt.Errorf("agent: finalize plugin %q: %w", p.Name(), err)
				break
			}
		}
	}

	// 4. Teardown (always).
	teardownErr := runTeardowns()

	switch {
	case loopErr != nil:
		return result, loopErr
	case verifyErr != nil:
		return result, verifyErr
	case finalizeErr != nil:
		return result, finalizeErr
	case teardownErr != nil:
		return result, fmt.Errorf("agent: teardown: %w", teardownErr)
	}
	return result, nil
}

// recordEvent updates the run context from one streamed event: session id from
// system events, and changed files from file-mutating tool_use events (reusing
// history's canonical tool table). Paths are normalized to repo-relative.
func (r *Runner) recordEvent(rc *RunContext, ev ai.Event) {
	switch ev.Kind {
	case ai.EventSystem:
		if ev.SessionID != "" {
			rc.SessionID = ev.SessionID
		}
	case ai.EventToolUse:
		path, ok := mutatedPath(ev)
		if !ok {
			return
		}
		rel := path
		if rc.Repo != "" {
			rel = claude.RelativePath(path, rc.Repo)
		}
		rc.ChangedFiles = addUnique(rc.ChangedFiles, rel)
	}
}

// runVerifiers runs every VerifyPlugin; the run is OK only if all are OK, and
// each failing plugin's feedback is concatenated for the next prompt.
func (r *Runner) runVerifiers(rc *RunContext, iter *ai.LoopIteration) (Verdict, error) {
	overall := Verdict{OK: true}
	var reasons, feedbacks []string
	for _, p := range r.Plugins {
		vp, ok := p.(VerifyPlugin)
		if !ok {
			continue
		}
		v, err := vp.Verify(rc, iter)
		if err != nil {
			return Verdict{}, fmt.Errorf("verify plugin %q: %w", p.Name(), err)
		}
		if !v.OK {
			overall.OK = false
			if v.Reason != "" {
				reasons = append(reasons, p.Name()+": "+v.Reason)
			}
			if v.Feedback != "" {
				feedbacks = append(feedbacks, v.Feedback)
			}
		}
	}
	overall.Reason = strings.Join(reasons, "; ")
	overall.Feedback = strings.Join(feedbacks, "\n\n")
	return overall, nil
}

// mutatedPath extracts the file an Edit/Write/MultiEdit/NotebookEdit tool_use
// wrote to, reusing history's canonical tool→input-key table.
func mutatedPath(ev ai.Event) (string, bool) {
	files := history.ModifiedFiles([]history.ToolUse{{Tool: ev.Tool, Input: ev.Input}})
	if len(files) == 0 {
		return "", false
	}
	return files[0], true
}

func addUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}
