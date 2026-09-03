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

// HookContext and its Notify/Emit/Workspace methods live in hook_context.go;
// Scope lives in scope.go; the hook interfaces (PreRun, Verify, Post, Output),
// VerifyResult and WorkspaceIsolator live in hooks.go.

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
	if err := r.Request.ValidateRunnable(); err != nil {
		return zero, fmt.Errorf("agent: %w", err)
	}
	// The spec's budget.timeout bounds the whole run, not one model call. Only
	// pkg/cli applied it before, so every caller driving the Runner directly
	// (gavel's `pr status --ai-fix`) ran unbounded and a wedged turn could hang
	// indefinitely with a 45m ceiling declared and silently ignored.
	timeout, err := r.Request.Budget.ParseTimeout()
	if err != nil {
		return zero, fmt.Errorf("agent: %w", err)
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
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
	// Hook notices join the model's own events on one stream, attributed to the
	// iteration whose boundary the hook is standing on — hc.Iteration still names
	// the turn that just completed when PhaseTurn dispatches.
	hc.emit = func(ev ai.Event) {
		if r.OnEvent != nil {
			r.OnEvent(hc.Iteration, ev)
		}
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

	// One rule, shared with every caller: api.Spec.IsVerifyOnly. Deciding here
	// with a second test of its own ("is the user prompt blank?") is how a request
	// carrying attachments or a message history got a provider built for it and
	// then never generated — reporting a pass with nothing verified.
	verifyOnly := r.Request.IsVerifyOnly()
	var runErr error
	if verifyOnly {
		runErr = r.runVerifyOnce(hc, &result)
	} else if r.Provider == nil {
		runErr = fmt.Errorf("agent: Provider is required")
	} else {
		runErr = r.runLoop(ctx, hc, &result)
	}

	hc.Failed = runErr != nil
	hc.Verified = VerifyPassed(result.Verdicts)
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
			// RunUntil calls BuildRequest once more after the final executed turn
			// and only then notices it is out of iterations, so advancing the
			// index here unconditionally left the run — and every PhaseAgent /
			// PhaseRun hook and event after it — attributed to a turn that never
			// ran. Name the turn only when it is really about to execute.
			if iter < maxIter {
				hc.Iteration = iter
			}
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

// VerifyPassed reports whether the run's last verify verdict passed, or
// trivially true when no Verify hooks ran at all. It is the single definition of
// "did this run verify": Runner.Run sets HookContext.Verified from it, and
// promptrun.Passed is it — a caller reading Result.Verdicts must never re-derive
// the rule, because a second copy is free to drift into calling a failed run
// green.
//
// Reading only the last verdict is sound because verify() stops a round at
// its first failure: within any round a failure is that round's final
// verdict, so the last entry of the accumulated list is always the final
// round's outcome. Earlier invalid entries are the history of rounds whose
// feedback drove a retry, not the run's result.
func VerifyPassed(verdicts []VerifyResult) bool {
	if len(verdicts) == 0 {
		return true
	}
	return verdicts[len(verdicts)-1].Valid
}

// verify runs the Verify hooks in declaration order and stops at the first
// failure (issue #40 R5.1). Stopping is not just economy — a cheap failing
// gate short-circuits the expensive judges behind it — it is what keeps
// VerifyPassed's last-verdict read correct: continuing past a failure lets a
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
		for _, path := range mutatedPaths(ev) {
			rel := path
			if ws.Repo != "" {
				rel = claude.RelativePath(path, ws.Repo)
			}
			ws.Changed = addUnique(ws.Changed, rel)
		}
	}
}

// mutatedPaths extracts every file one tool_use wrote to, reusing history's
// canonical footprint table (Edit/Write/NotebookEdit name a file; apply_patch
// carries a whole patch; a Bash command is analysed for redirects, sed -i, mv
// and rm).
//
// All of them, not just the first: a patch is the common shape here and it
// routinely touches several files at once. Recording one of them left the rest
// unattributable, and a commit hook staging only what the run is recorded as
// having changed would then drop them — leaving a turn's work half-committed and
// the remainder dirty with nothing to explain it.
func mutatedPaths(ev ai.Event) []string {
	return history.ModifiedFiles([]history.ToolUse{{Tool: ev.Tool, Input: ev.Input}})
}

func addUnique(list []string, v string) []string {
	for _, existing := range list {
		if existing == v {
			return list
		}
	}
	return append(list, v)
}
