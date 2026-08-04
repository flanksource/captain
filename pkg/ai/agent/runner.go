// Package agent composes a generate→verify run out of generic hooks.
//
// A Runner[T] drives an iterative AI run: PreRun hooks (e.g. a git worktree) run
// once before the loop; Verify hooks vote after every iteration and, when a run
// fails, return the exact next request to re-run; Post hooks (e.g. commit +
// worktree teardown) run at the lifecycle phases they declare; an optional
// Output hook produces the typed final result T. Every hook receives a
// HookContext carrying the rendered request and the accumulating api.Response —
// whose Workspace holds the run's cwd/git/changed/commits/plan state.
//
//	PreRun → loop{ generate → [turn] → verify } → [agent] → [run] → Output
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons/merge"
)

// fallbackLog is used when ctx carries no task-scoped logger (ai.ContextWithLogger).
var fallbackLog = logger.GetLogger("agent")

// Scope controls how much hooks act on. ScopeChanged restricts them to the files
// the agent edited; ScopeAll lets each act on the whole tree.
type Scope string

const (
	ScopeChanged Scope = "changed"
	ScopeAll     Scope = "all"
)

// AllScopes lists every scope in canonical order.
func AllScopes() []Scope { return []Scope{ScopeAll, ScopeChanged} }

// Valid reports whether s is one of the supported scopes.
func (s Scope) Valid() bool {
	for _, x := range AllScopes() {
		if s == x {
			return true
		}
	}
	return false
}

// ScopeList renders the supported scopes as a comma-separated string.
func ScopeList() string {
	parts := make([]string, len(AllScopes()))
	for i, s := range AllScopes() {
		parts[i] = string(s)
	}
	return strings.Join(parts, ", ")
}

// ParseScope resolves a CLI/flag value into a Scope, defaulting empty to ScopeAll.
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

// HookContext carries what hooks read/mutate: the request for this iteration and
// the accumulating response, whose Workspace holds the run's working-dir state.
type HookContext struct {
	context.Context
	// Request is the CURRENT request: PreRun hooks rewrite it (setup replaces the
	// checkout it performed with where that landed), and the runner replaces it
	// wholesale with each verify-driven retry.
	Request *ai.Request
	// Original is the request the run started from, before any hook rewrote it.
	// It is the only place a Post hook can read what the run was *asked* to do —
	// which repo to check out, which branch to isolate on — while Request says
	// where that ended up. Cloned once by Run, so mutating it affects nothing.
	Original  ai.Request
	Response  *ai.Response
	Iteration int
	Scope     Scope

	// Hooks is the run's hook list, so a hook can detect an incompatible peer
	// rather than silently competing with it (see EnsureSingleIsolator).
	Hooks []any

	// Phase is the boundary currently being dispatched; it is only meaningful
	// inside a Post hook, which also receives it as an argument.
	Phase Phase
	// Turn is the loop iteration that just completed. Set only during PhaseTurn;
	// nil everywhere else.
	Turn *ai.LoopIteration

	// Verified and Failed describe the run's outcome so PhaseAgent/PhaseRun
	// hooks (e.g. the worktree merge/cleanup gate) can act on it. Runner.Run
	// sets both right after the loop ends; they are meaningless beforehand, and
	// in particular are not yet settled during PhaseTurn.
	//
	// Verified mirrors VerifyPassed(result.Verdicts) — true when the last verify
	// verdict passed, or trivially true when no Verify hooks ran at all.
	Verified bool
	// Failed is true when the generate/verify run itself returned an error
	// (a provider failure, not a failing verdict).
	Failed bool
}

// Workspace returns the run's working-dir state, allocating it if needed (so it
// is never nil for a hook to read/mutate).
func (hc *HookContext) Workspace() *api.Workspace {
	if hc.Response.Workspace == nil {
		hc.Response.Workspace = &api.Workspace{}
	}
	return hc.Response.Workspace
}

// WorkspaceIsolator is implemented by a hook that relocates the run into its own
// working tree — a git worktree from Spec.Setup.Checkout, or the `wt` worktree
// plugin. Two of them in one run create two trees and use one, so each declares
// itself here and calls EnsureSingleIsolator before acting.
type WorkspaceIsolator interface {
	Name() string
	// IsolatesWorkspace reports whether this hook will relocate the run given
	// hc's request; a hook whose isolation is configured on the spec answers
	// from hc.Request rather than from its own fields.
	IsolatesWorkspace(*HookContext) bool
}

// EnsureSingleIsolator fails when more than one registered hook would relocate
// the run into its own working tree. Silently creating two and working in one is
// the failure this exists to prevent: the run edits a tree nobody merges.
func (hc *HookContext) EnsureSingleIsolator() error {
	var names []string
	for _, h := range hc.Hooks {
		if iso, ok := h.(WorkspaceIsolator); ok && iso.IsolatesWorkspace(hc) {
			names = append(names, iso.Name())
		}
	}
	if len(names) > 1 {
		return fmt.Errorf("agent: hooks %s each isolate the run in their own working tree; register exactly one", strings.Join(names, " and "))
	}
	return nil
}

// VerifyResult is a Verify hook's judgement on an iteration. When !Valid, Retry
// (if non-nil) is the exact next request to run — the hook bakes its feedback
// into that request's prompt. Output carries structured verify output.
type VerifyResult struct {
	Valid  bool
	Retry  *ai.Request
	Output any
}

// PreRun runs once before the loop.
type PreRun interface {
	Name() string
	PreRun(*HookContext) error
}

// Verify runs after each completed iteration and votes.
type Verify interface {
	Name() string
	Verify(*HookContext) (VerifyResult, error)
}

// Post runs after a lifecycle phase completes (commit, teardown, checkpointing).
// A hook declares which phases it handles via Phases(); the runner dispatches
// only those, and always — including on the failure path, so a hook can make
// work durable or tear down after an error.
type Post interface {
	Name() string
	Phases() []Phase
	Post(*HookContext, Phase) error
}

// Output produces the workflow's typed final result. Optional; when absent the
// runner leaves Result.Output at its zero value.
type Output[T any] interface {
	Name() string
	Output(*HookContext) (T, error)
}

// Runner drives a generate→verify loop composed of hooks, producing a typed
// result T. Hooks is a heterogeneous list; each element may implement any of
// PreRun/Verify/Post/Output[T].
type Runner[T any] struct {
	Provider      ai.StreamingProvider
	Request       ai.Request // the initial rendered request
	Hooks         []any
	MaxIterations int // 0 ⇒ 1
	Repo, Cwd     string
	Scope         Scope
	OnEvent       func(iter int, ev ai.Event) // live progress tap
}

// Result bundles the run outcome. Response carries the final text/structured
// data + Workspace; Output is the typed final result.
type Result[T any] struct {
	Response *ai.Response
	Output   T
	Verdicts []VerifyResult
	Loop     *ai.LoopResult
}

// Run executes the pipeline: PreRun → loop(generate + [turn] + verify) |
// verify-only → [agent] → [run] → Output, where each [phase] dispatches to the
// Post hooks declaring it and always runs, including on the failure path. An
// empty prompt body runs verify-only (no generation, and so no turn phase); no
// Verify hooks runs generate-only.
func (r *Runner[T]) Run(ctx context.Context) (Result[T], error) {
	var zero Result[T]
	scope := r.Scope
	if scope == "" {
		scope = ScopeAll
	}
	resp := &ai.Response{Workspace: &api.Workspace{Repo: r.Repo, Cwd: r.Cwd}}
	hc := &HookContext{
		Context: ctx,
		Request: &r.Request,
		// Cloned before the PreRun loop, which is the last moment the request
		// still describes what the run was asked to do.
		Original: merge.Clone(r.Request, api.MergePolicy()),
		Response: resp,
		Scope:    scope,
		Hooks:    r.Hooks,
	}
	result := Result[T]{Response: resp}

	for _, h := range r.Hooks {
		if pr, ok := h.(PreRun); ok {
			if err := pr.PreRun(hc); err != nil {
				hc.Failed = true
				// The loop never started, so there is no agent phase to close —
				// only teardown.
				_ = r.post(hc, PhaseRun)
				return zero, fmt.Errorf("agent: preRun %q: %w", pr.Name(), err)
			}
		}
	}

	verifyOnly := strings.TrimSpace(r.Request.Prompt.User) == ""
	var runErr error
	if verifyOnly {
		runErr = r.runVerifyOnce(hc, &result)
	} else if r.Provider == nil {
		runErr = fmt.Errorf("agent: Provider is required")
	} else {
		runErr = r.runLoop(ctx, hc, &result)
	}

	hc.Failed = runErr != nil
	hc.Verified = verifyPassed(result.Verdicts)
	// PhaseAgent closes out the loop while the working tree is still live (the
	// last point a hook can commit an isolated worktree); PhaseRun then tears it
	// down. Both run even when the loop failed — that is what makes a failed
	// run's work durable.
	postErr := errors.Join(r.post(hc, PhaseAgent), r.post(hc, PhaseRun))
	if err := errors.Join(runErr, postErr); err != nil {
		return result, err
	}

	for _, h := range r.Hooks {
		if oh, ok := h.(Output[T]); ok {
			out, err := oh.Output(hc)
			if err != nil {
				return result, fmt.Errorf("agent: output %q: %w", oh.Name(), err)
			}
			result.Output = out
			break
		}
	}
	return result, nil
}

// post dispatches one phase to every Post hook that declares it, always, in
// hook order; returns the first error but never short-circuits, so a failing
// commit hook cannot skip the worktree teardown that follows it.
func (r *Runner[T]) post(hc *HookContext, phase Phase) error {
	var firstErr error
	prevPhase, prevTurn := hc.Phase, hc.Turn
	hc.Phase = phase
	defer func() { hc.Phase, hc.Turn = prevPhase, prevTurn }()
	for _, h := range r.Hooks {
		ph, ok := h.(Post)
		if !ok || !handlesPhase(ph, phase) {
			continue
		}
		if err := ph.Post(hc, phase); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("agent: %s hook %q: %w", phase, ph.Name(), err)
		}
	}
	return firstErr
}

func handlesPhase(h Post, phase Phase) bool {
	for _, p := range h.Phases() {
		if p == phase {
			return true
		}
	}
	return false
}

// runVerifyOnce runs the Verify hooks a single time against the current state
// (no generation) — e.g. scoring already-committed work.
func (r *Runner[T]) runVerifyOnce(hc *HookContext, result *Result[T]) error {
	verdicts, _, _, err := r.verify(hc)
	if err != nil {
		return err
	}
	result.Verdicts = append(result.Verdicts, verdicts...)
	return nil
}

// runLoop drives ai.RunUntil; verifiers run in the BuildRequest hook between
// iterations, and a failing verdict's Retry becomes the next request.
func (r *Runner[T]) runLoop(ctx context.Context, hc *HookContext, result *Result[T]) error {
	log := ai.LoggerFromContext(ctx, fallbackLog)
	maxIter := r.MaxIterations
	if maxIter <= 0 {
		maxIter = 1
	}
	req := r.Request
	var responseErr, verifyErr, turnErr error

	loop, loopErr := ai.RunUntil(ctx, ai.LoopOptions{
		Provider:      r.Provider,
		MaxIterations: maxIter,
		MaxCostUSD:    r.Request.Budget.Cost, // enforce the USD budget across iterations
		OnEvent: func(iter int, ev ai.Event) {
			r.recordEvent(hc, ev)
			if r.OnEvent != nil {
				r.OnEvent(iter, ev)
			}
		},
		BuildRequest: func(iter int, prev *ai.LoopIteration) (ai.Request, bool) {
			if prev != nil {
				if err := r.updateResponse(hc, prev); err != nil {
					responseErr = err
					return ai.Request{}, false
				}
				// PhaseTurn fires here — after the turn is folded into the
				// response, before its verifiers vote, and before every early
				// return below. RunUntil calls BuildRequest once more after the
				// final executed turn (including the call that stops the loop),
				// so this placement covers every turn including the last.
				// Dispatching after verify() would skip turns whose verify
				// errors and would lose the property that a turn's work is
				// durable even when verification then fails.
				hc.Turn = prev
				if err := r.post(hc, PhaseTurn); err != nil {
					turnErr = err
					return ai.Request{}, false
				}
				hc.Turn = nil
				verdicts, retry, allValid, err := r.verify(hc)
				if err != nil {
					verifyErr = err
					return ai.Request{}, false
				}
				result.Verdicts = append(result.Verdicts, verdicts...)
				if allValid {
					log.Tracef("agent: iteration %d verified OK; stopping", iter)
					return ai.Request{}, false
				}
				if retry == nil {
					return ai.Request{}, false // failed, no retry proposed
				}
				req = *retry
			}
			hc.Iteration = iter
			hc.Request = &req
			// A hook that relocated the run recorded where on the workspace;
			// propagate it, because a verify-driven retry is built fresh and
			// would otherwise point at the tree the run started in. An empty
			// workspace cwd means nothing relocated the run, so the spec's own
			// cwd stands rather than being blanked.
			if cwd := hc.Workspace().Cwd; cwd != "" {
				req.SetCwd(cwd)
			}
			return req, true
		},
	})
	result.Loop = loop
	if loopErr != nil {
		return loopErr
	}
	if loop != nil && loop.StopReason == "max-cost" {
		return fmt.Errorf("%w: spent $%.4f of $%.4f budget", ai.ErrBudgetExceeded, loop.TotalCost, r.Request.Budget.Cost)
	}
	if responseErr != nil {
		return responseErr
	}
	if turnErr != nil {
		return turnErr
	}
	return verifyErr
}

// verifyPassed reports whether the run's last verify verdict passed, or
// trivially true when no Verify hooks ran at all. Runner.Run uses it to set
// HookContext.Verified for Post hooks; mirrors pkg/cli's own verifyPassed,
// which summarizes the same Result.Verdicts for CLI output.
//
// Reading only the last verdict is sound because verify() stops a round at
// its first failure: within any round a failure is that round's final
// verdict, so the last entry of the accumulated list is always the final
// round's outcome. Earlier invalid entries are the history of rounds whose
// feedback drove a retry, not the run's result.
func verifyPassed(verdicts []VerifyResult) bool {
	if len(verdicts) == 0 {
		return true
	}
	return verdicts[len(verdicts)-1].Valid
}

// verify runs the Verify hooks in declaration order and stops at the first
// failure (issue #40 R5.1). Stopping is not just economy — a cheap failing
// gate short-circuits the expensive judges behind it — it is what keeps
// verifyPassed's last-verdict read correct: continuing past a failure lets a
// later passing hook become the round's final verdict and mask the failure.
// retry is the failing hook's proposed next request.
func (r *Runner[T]) verify(hc *HookContext) (verdicts []VerifyResult, retry *ai.Request, allValid bool, err error) {
	for _, h := range r.Hooks {
		v, ok := h.(Verify)
		if !ok {
			continue
		}
		res, verr := v.Verify(hc)
		if verr != nil {
			return verdicts, nil, false, fmt.Errorf("agent: verify %q: %w", v.Name(), verr)
		}
		verdicts = append(verdicts, res)
		if !res.Valid {
			return verdicts, res.Retry, false, nil
		}
	}
	return verdicts, nil, true, nil
}

// updateResponse folds one completed iteration into the accumulating response:
// its assembled text, structured data, usage, and session id.
func (r *Runner[T]) updateResponse(hc *HookContext, prev *ai.LoopIteration) error {
	ws := hc.Workspace()
	if prev.SessionID != "" {
		ws.SessionID = prev.SessionID
	}
	var text strings.Builder
	for _, ev := range prev.Events {
		outcome, err := ai.TerminalOutcomeFromEvent(ev)
		if err != nil {
			return fmt.Errorf("agent: invalid terminal outcome: %w", err)
		}
		if outcome != nil {
			hc.Response.TerminalOutcome = outcome
		}
		switch ev.Kind {
		case ai.EventText:
			text.WriteString(ev.Text)
		case ai.EventResult:
			if len(ev.StructuredData) > 0 {
				hc.Response.StructuredData = ev.StructuredData
			}
		}
	}
	hc.Response.Text = text.String()
	hc.Response.Usage = prev.Usage
	return nil
}

// recordEvent updates the workspace from one streamed event: session id and the
// set of files the agent changed (repo-relative).
func (r *Runner[T]) recordEvent(hc *HookContext, ev ai.Event) {
	ws := hc.Workspace()
	switch ev.Kind {
	case ai.EventSystem:
		if ev.SessionID != "" {
			ws.SessionID = ev.SessionID
		}
	case ai.EventToolUse:
		path, ok := mutatedPath(ev)
		if !ok {
			return
		}
		rel := path
		if ws.Repo != "" {
			rel = claude.RelativePath(path, ws.Repo)
		}
		ws.Changed = addUnique(ws.Changed, rel)
	}
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
