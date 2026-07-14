// Package agent composes a generate→verify run out of generic hooks.
//
// A Runner[T] drives an iterative AI run: PreRun hooks (e.g. a git worktree) run
// once before the loop; Verify hooks vote after every iteration and, when a run
// fails, return the exact next request to re-run; PostRun hooks (e.g. commit +
// worktree teardown) run once after; an optional Output hook produces the typed
// final result T. Every hook receives a HookContext carrying the rendered
// request and the accumulating api.Response — whose Workspace holds the run's
// cwd/git/changed/commits/plan state.
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/commons/logger"
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
	Request   *ai.Request
	Response  *ai.Response
	Iteration int
	Scope     Scope

	// Verified and Failed describe the run's outcome so PostRun hooks (e.g. the
	// worktree merge/cleanup gate) can act on it. Runner.Run sets both right
	// before invoking PostRun hooks; they are meaningless beforehand.
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

// PostRun runs once after the loop, always (commit + teardown).
type PostRun interface {
	Name() string
	PostRun(*HookContext) error
}

// Output produces the workflow's typed final result. Optional; when absent the
// runner leaves Result.Output at its zero value.
type Output[T any] interface {
	Name() string
	Output(*HookContext) (T, error)
}

// Runner drives a generate→verify loop composed of hooks, producing a typed
// result T. Hooks is a heterogeneous list; each element may implement any of
// PreRun/Verify/PostRun/Output[T].
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

// Run executes the pipeline: PreRun → loop(generate + verify) | verify-only →
// PostRun (always) → Output. An empty prompt body runs verify-only (no
// generation); no Verify hooks runs generate-only.
func (r *Runner[T]) Run(ctx context.Context) (Result[T], error) {
	var zero Result[T]
	scope := r.Scope
	if scope == "" {
		scope = ScopeAll
	}
	resp := &ai.Response{Workspace: &api.Workspace{Repo: r.Repo, Cwd: r.Cwd}}
	hc := &HookContext{Context: ctx, Request: &r.Request, Response: resp, Scope: scope}
	result := Result[T]{Response: resp}

	for _, h := range r.Hooks {
		if pr, ok := h.(PreRun); ok {
			if err := pr.PreRun(hc); err != nil {
				hc.Failed = true
				_ = r.runPostRun(hc) // best-effort teardown
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
	postErr := r.runPostRun(hc)
	if runErr != nil {
		return result, runErr
	}
	if postErr != nil {
		return result, postErr
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

// runPostRun runs every PostRun hook, always; returns the first error.
func (r *Runner[T]) runPostRun(hc *HookContext) error {
	var firstErr error
	for _, h := range r.Hooks {
		if pr, ok := h.(PostRun); ok {
			if err := pr.PostRun(hc); err != nil && firstErr == nil {
				firstErr = fmt.Errorf("agent: postRun %q: %w", pr.Name(), err)
			}
		}
	}
	return firstErr
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
	var responseErr, verifyErr error

	loop, loopErr := ai.RunUntil(ctx, ai.LoopOptions{
		Provider:      r.Provider,
		MaxIterations: maxIter,
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
			req.SetCwd(hc.Workspace().Cwd)
			return req, true
		},
	})
	result.Loop = loop
	if loopErr != nil {
		return loopErr
	}
	if responseErr != nil {
		return responseErr
	}
	return verifyErr
}

// verifyPassed reports whether the run's last verify verdict passed, or
// trivially true when no Verify hooks ran at all. Runner.Run uses it to set
// HookContext.Verified for PostRun hooks; mirrors pkg/cli's own verifyPassed,
// which summarizes the same Result.Verdicts for CLI output.
func verifyPassed(verdicts []VerifyResult) bool {
	if len(verdicts) == 0 {
		return true
	}
	return verdicts[len(verdicts)-1].Valid
}

// verify runs every Verify hook; allValid is true only if all passed. retry is
// the first failing hook's proposed next request.
func (r *Runner[T]) verify(hc *HookContext) (verdicts []VerifyResult, retry *ai.Request, allValid bool, err error) {
	allValid = true
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
			allValid = false
			if retry == nil && res.Retry != nil {
				retry = res.Retry
			}
		}
	}
	return verdicts, retry, allValid, nil
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
