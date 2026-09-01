package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/shell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider emits a scripted set of events per iteration.
type fakeProvider struct {
	events func(iter int) []ai.Event
	calls  int
}

func (f *fakeProvider) GetModel() string       { return "fake" }
func (f *fakeProvider) GetRuntime() ai.Runtime { return ai.RuntimeOf(ai.Anthropic, ai.ModeAgent) }
func (f *fakeProvider) Execute(context.Context, ai.Request) (*ai.Response, error) {
	return &ai.Response{}, nil
}
func (f *fakeProvider) ExecuteStream(_ context.Context, _ ai.Request) (<-chan ai.Event, error) {
	iter := f.calls
	f.calls++
	ch := make(chan ai.Event, 16)
	go func() {
		defer close(ch)
		for _, ev := range f.events(iter) {
			ch <- ev
		}
	}()
	return ch, nil
}

func TestRunner_CapturesSessionAndChangedFiles(t *testing.T) {
	prov := &fakeProvider{events: func(int) []ai.Event {
		return []ai.Event{
			{Kind: ai.EventSystem, SessionID: "sess-1"},
			{Kind: ai.EventToolUse, Tool: "Edit", Input: map[string]any{"file_path": "/repo/pkg/a.go"}},
			{Kind: ai.EventToolUse, Tool: "Bash", Input: map[string]any{"command": "ls"}}, // not a mutation
			{Kind: ai.EventToolUse, Tool: "Write", Input: map[string]any{"file_path": "/repo/pkg/b.go"}},
			{Kind: ai.EventResult, Success: true, CostUSD: 0.01},
		}
	}}

	r := &Runner[string]{
		Provider: prov,
		Repo:     "/repo",
		Request:  ai.Request{Prompt: api.Prompt{User: "go"}},
	}
	res, err := r.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, res.Response.Workspace)
	assert.Equal(t, "sess-1", res.Response.Workspace.SessionID)
	assert.Equal(t, []string{"pkg/a.go", "pkg/b.go"}, res.Response.Workspace.Changed)
	assert.Equal(t, 1, prov.calls, "no verify hooks ⇒ exactly one iteration")
}

// TestRunner_RecordsEveryFileOneToolWrote covers the shapes that touch several
// files in a single call. Recording only the first left the rest unattributable,
// and a commit hook staging what the run is recorded as having changed then
// dropped them: half the turn committed, the remainder dirty and unexplained.
func TestRunner_RecordsEveryFileOneToolWrote(t *testing.T) {
	patch := "*** Begin Patch\n" +
		"*** Update File: /repo/pkg/one.go\n" +
		"*** Update File: /repo/pkg/two.go\n" +
		"*** Delete File: /repo/pkg/three.go\n" +
		"*** End Patch\n"
	prov := &fakeProvider{events: func(int) []ai.Event {
		return []ai.Event{
			{Kind: ai.EventToolUse, Tool: "apply_patch", Input: map[string]any{"input": patch}},
			// A shell command writing two files is the same problem in the other
			// tool shape.
			{Kind: ai.EventToolUse, Tool: "Bash", Input: map[string]any{
				"command": "echo x > /repo/pkg/four.go && rm /repo/pkg/five.go",
			}},
			{Kind: ai.EventResult, Success: true},
		}
	}}

	r := &Runner[string]{
		Provider: prov,
		Repo:     "/repo",
		Request:  ai.Request{Prompt: api.Prompt{User: "go"}},
	}
	res, err := r.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, res.Response.Workspace)
	assert.ElementsMatch(t,
		[]string{"pkg/one.go", "pkg/two.go", "pkg/three.go", "pkg/four.go", "pkg/five.go"},
		res.Response.Workspace.Changed)
}

func TestRunner_CapturesNativePlanOutcome(t *testing.T) {
	prov := &fakeProvider{events: func(int) []ai.Event {
		return []ai.Event{
			{Kind: ai.EventToolUse, Tool: "ExitPlanMode", Input: map[string]any{
				"plan":         "1. Inspect\n2. Implement",
				"planFilePath": "/repo/.claude/plans/example.md",
			}},
			{Kind: ai.EventResult, Success: true},
		}
	}}

	result, err := (&Runner[string]{
		Provider: prov,
		Request:  ai.Request{Prompt: api.Prompt{User: "plan"}},
	}).Run(context.Background())

	require.NoError(t, err)
	require.NotNil(t, result.Response.TerminalOutcome)
	assert.Equal(t, ai.TerminalOutcomePlan, result.Response.TerminalOutcome.Kind)
	require.NotNil(t, result.Response.TerminalOutcome.Plan)
	assert.Equal(t, "1. Inspect\n2. Implement", result.Response.TerminalOutcome.Plan.Content)
	assert.Equal(t, "/repo/.claude/plans/example.md", result.Response.TerminalOutcome.Plan.Path)
}

func TestRunner_FailsOnMalformedNativePlanOutcome(t *testing.T) {
	prov := &fakeProvider{events: func(int) []ai.Event {
		return []ai.Event{
			{Kind: ai.EventToolUse, Tool: "ExitPlanMode", Input: map[string]any{"planFilePath": "/repo/plan.md"}},
			{Kind: ai.EventResult, Success: true},
		}
	}}

	_, err := (&Runner[string]{
		Provider: prov,
		Request:  ai.Request{Prompt: api.Prompt{User: "plan"}},
	}).Run(context.Background())

	require.ErrorContains(t, err, "ExitPlanMode")
	require.ErrorContains(t, err, "plan is required")
}

func TestRunner_VerifyDrivesRerunThenStops(t *testing.T) {
	prov := &fakeProvider{events: func(int) []ai.Event {
		return []ai.Event{{Kind: ai.EventResult, Success: true}}
	}}

	var verifyCalls int
	r := &Runner[string]{
		Provider:      prov,
		Scope:         ScopeAll,
		MaxIterations: 3,
		Request:       ai.Request{Prompt: api.Prompt{User: "go"}},
		Hooks: []any{
			verifyHook{name: "lint", fn: func(hc *HookContext) (VerifyResult, error) {
				verifyCalls++
				if verifyCalls == 1 {
					// fail: the hook returns the exact next request to re-run.
					next := *hc.Request
					next.Prompt.User = hc.Request.Prompt.User + "\n\nfix line 7"
					return VerifyResult{Valid: false, Retry: &next}, nil
				}
				return VerifyResult{Valid: true}, nil
			}},
		},
	}

	res, err := r.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, prov.calls, "re-run once after the first failing verify")
	assert.Equal(t, 2, verifyCalls)
	require.Len(t, res.Verdicts, 2)
	assert.False(t, res.Verdicts[0].Valid)
	assert.True(t, res.Verdicts[1].Valid)
}

func TestRunner_PhaseOrder(t *testing.T) {
	prov := &fakeProvider{events: func(int) []ai.Event {
		return []ai.Event{{Kind: ai.EventResult, Success: true}}
	}}

	var log []string
	r := &Runner[string]{
		Provider: prov,
		Request:  ai.Request{Prompt: api.Prompt{User: "go"}},
		Hooks:    []any{&lifecycleHook{log: &log}},
	}
	_, err := r.Run(context.Background())
	require.NoError(t, err)
	// PreRun before the loop, then one turn, then agent and run teardown. The
	// single turn still emits a turn phase: RunUntil calls BuildRequest once
	// more after the last executed iteration.
	assert.Equal(t, []string{"preRun", "turn", "agent", "run"}, log)
}

func TestRunner_TurnPhaseFiresPerTurnBeforeVerifiers(t *testing.T) {
	prov := &fakeProvider{events: func(int) []ai.Event {
		return []ai.Event{{Kind: ai.EventResult, Success: true}}
	}}

	var log []string
	var turns []int
	var verifyCalls int
	r := &Runner[string]{
		Provider:      prov,
		MaxIterations: 3,
		Request:       ai.Request{Prompt: api.Prompt{User: "go"}},
		Hooks: []any{
			&lifecycleHook{log: &log, onTurn: func(hc *HookContext) {
				require.NotNil(t, hc.Turn, "a turn dispatch carries the iteration that just completed")
				turns = append(turns, hc.Turn.Iteration)
				assert.Equal(t, hc.Turn.Iteration, hc.Iteration, "the context still describes the completed turn, not the next one")
			}},
			verifyHook{name: "lint", fn: func(hc *HookContext) (VerifyResult, error) {
				log = append(log, "verify")
				verifyCalls++
				assert.Nil(t, hc.Turn, "Turn is scoped to the turn phase")
				if verifyCalls < 3 {
					next := *hc.Request
					return VerifyResult{Valid: false, Retry: &next}, nil
				}
				return VerifyResult{Valid: true}, nil
			}},
		},
	}

	_, err := r.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, prov.calls)
	// The last turn is the one that matters: RunUntil asks for a fourth request
	// only to be told to stop, and the turn phase has to fire on that call or the
	// run's final piece of work is never made durable.
	assert.Equal(t, []int{0, 1, 2}, turns, "every turn including the terminal one")
	// A turn commits before its verdict is in, so work survives a failing verify.
	assert.Equal(t, []string{"preRun", "turn", "verify", "turn", "verify", "turn", "verify", "agent", "run"}, log)
}

func TestRunner_VerifyOnlyEmitsNoTurnPhase(t *testing.T) {
	var log []string
	// Empty prompt body ⇒ verify-only: nothing generated, so no turn boundary.
	r := &Runner[string]{
		Hooks: []any{
			&lifecycleHook{log: &log},
			verifyHook{name: "score", fn: func(*HookContext) (VerifyResult, error) {
				return VerifyResult{Valid: true}, nil
			}},
		},
	}

	_, err := r.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{"preRun", "agent", "run"}, log)
}

func TestRunner_AgentPhaseFiresOnceAfterATurnErrors(t *testing.T) {
	prov := &fakeProvider{events: func(iter int) []ai.Event {
		if iter == 1 {
			return []ai.Event{{Kind: ai.EventError, Error: "provider exploded"}}
		}
		return []ai.Event{{Kind: ai.EventResult, Success: true}}
	}}

	var log []string
	r := &Runner[string]{
		Provider:      prov,
		MaxIterations: 3,
		Request:       ai.Request{Prompt: api.Prompt{User: "go"}},
		Hooks: []any{
			&lifecycleHook{log: &log},
			verifyHook{name: "lint", fn: func(hc *HookContext) (VerifyResult, error) {
				next := *hc.Request
				return VerifyResult{Valid: false, Retry: &next}, nil
			}},
		},
	}

	_, err := r.Run(context.Background())
	require.ErrorContains(t, err, "provider exploded")
	// The second turn died inside RunUntil, which returns without asking for
	// another request — so it never reaches a turn boundary. The agent phase is
	// what sweeps that remainder, and it still fires exactly once.
	assert.Equal(t, []string{"preRun", "turn", "agent", "run"}, log)
}

func TestRunner_PhasesDispatchInHookOrder(t *testing.T) {
	prov := &fakeProvider{events: func(int) []ai.Event {
		return []ai.Event{{Kind: ai.EventResult, Success: true}}
	}}

	var log []string
	r := &Runner[string]{
		Provider: prov,
		Request:  ai.Request{Prompt: api.Prompt{User: "go"}},
		Hooks: []any{
			&lifecycleHook{name: "commit", log: &log},
			&lifecycleHook{name: "worktree", log: &log},
		},
	}

	_, err := r.Run(context.Background())
	require.NoError(t, err)
	// Registration order decides who acts first within a phase, which is what
	// lets a commit hook squash its chain before a worktree hook merges the
	// branch away.
	assert.Equal(t, []string{
		"commit:preRun", "worktree:preRun",
		"commit:turn", "worktree:turn",
		"commit:agent", "worktree:agent",
		"commit:run", "worktree:run",
	}, log)
}

func TestRunner_FailingPostHookSurfacesWithoutSkippingTeardown(t *testing.T) {
	var log []string
	r := &Runner[string]{
		Provider: nil, // ⇒ the run itself fails
		Request:  ai.Request{Prompt: api.Prompt{User: "go"}},
		Hooks: []any{
			&lifecycleHook{name: "commit", log: &log, fail: map[Phase]error{PhaseAgent: errors.New("nothing staged")}},
			&lifecycleHook{name: "worktree", log: &log},
		},
	}

	_, err := r.Run(context.Background())
	require.Error(t, err)
	assert.ErrorContains(t, err, "Provider is required", "the run error survives a failing hook")
	assert.ErrorContains(t, err, "nothing staged", "a failing commit hook is not swallowed by the run error")
	assert.Equal(t, []string{
		"commit:preRun", "worktree:preRun",
		"commit:agent", "worktree:agent",
		"commit:run", "worktree:run",
	}, log, "a failing hook stops neither its own phase nor the teardown after it")
}

func TestRunner_FailingTurnHookStopsTheLoop(t *testing.T) {
	prov := &fakeProvider{events: func(int) []ai.Event {
		return []ai.Event{{Kind: ai.EventResult, Success: true}}
	}}

	var log []string
	var verifyCalls int
	r := &Runner[string]{
		Provider:      prov,
		MaxIterations: 3,
		Request:       ai.Request{Prompt: api.Prompt{User: "go"}},
		Hooks: []any{
			&lifecycleHook{name: "commit", log: &log, fail: map[Phase]error{PhaseTurn: errors.New("dirty index")}},
			verifyHook{name: "lint", fn: func(*HookContext) (VerifyResult, error) {
				verifyCalls++
				return VerifyResult{Valid: true}, nil
			}},
		},
	}

	_, err := r.Run(context.Background())
	require.ErrorContains(t, err, "dirty index")
	assert.Equal(t, 1, prov.calls, "no further generation on top of a turn that could not be committed")
	assert.Equal(t, 0, verifyCalls, "the turn phase short-circuits before its verifiers vote")
	assert.Equal(t, []string{"commit:preRun", "commit:turn", "commit:agent", "commit:run"}, log)
}

func TestRunner_RunPhaseSeesVerifiedAndFailed(t *testing.T) {
	cases := []struct {
		name         string
		provider     ai.StreamingProvider
		hook         verifyHook
		wantVerified bool
		wantFailed   bool
	}{
		{
			name:     "passing verify, no run error",
			provider: &fakeProvider{events: func(int) []ai.Event { return []ai.Event{{Kind: ai.EventResult, Success: true}} }},
			hook: verifyHook{name: "lint", fn: func(*HookContext) (VerifyResult, error) {
				return VerifyResult{Valid: true}, nil
			}},
			wantVerified: true,
			wantFailed:   false,
		},
		{
			name:     "failing verify with no retry, no run error",
			provider: &fakeProvider{events: func(int) []ai.Event { return []ai.Event{{Kind: ai.EventResult, Success: true}} }},
			hook: verifyHook{name: "lint", fn: func(*HookContext) (VerifyResult, error) {
				return VerifyResult{Valid: false}, nil
			}},
			wantVerified: false,
			wantFailed:   false,
		},
		{
			name:     "missing provider surfaces as a run error",
			provider: nil,
			hook: verifyHook{name: "lint", fn: func(*HookContext) (VerifyResult, error) {
				return VerifyResult{Valid: true}, nil
			}},
			wantVerified: true, // no verdicts recorded before the run error
			wantFailed:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var sawVerified, sawFailed bool
			outcome := &outcomeHook{onRun: func(hc *HookContext) {
				sawVerified = hc.Verified
				sawFailed = hc.Failed
			}}
			r := &Runner[string]{
				Provider: tc.provider,
				Request:  ai.Request{Prompt: api.Prompt{User: "go"}},
				Hooks:    []any{tc.hook, outcome},
			}
			_, _ = r.Run(context.Background())
			assert.Equal(t, tc.wantVerified, sawVerified, "hc.Verified")
			assert.Equal(t, tc.wantFailed, sawFailed, "hc.Failed")
		})
	}
}

func TestRunner_VerifyOnlySkipsGeneration(t *testing.T) {
	var verifyCalls int
	// Empty prompt body ⇒ verify-only: no provider, no generation loop.
	r := &Runner[string]{
		Scope: ScopeAll,
		Hooks: []any{
			verifyHook{name: "score", fn: func(*HookContext) (VerifyResult, error) {
				verifyCalls++
				return VerifyResult{Valid: true}, nil
			}},
		},
	}

	res, err := r.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, verifyCalls, "verify runs exactly once")
	require.Len(t, res.Verdicts, 1)
	assert.True(t, res.Verdicts[0].Valid)
	assert.Nil(t, res.Loop, "no generation loop in verify-only")
}

// --- test hooks -------------------------------------------------------------

type verifyHook struct {
	name string
	fn   func(*HookContext) (VerifyResult, error)
}

func (v verifyHook) Name() string                                 { return v.name }
func (v verifyHook) Verify(hc *HookContext) (VerifyResult, error) { return v.fn(hc) }

// outcomeHook records the HookContext.Verified/Failed values a run-phase hook
// observes, e.g. what a worktree.Plugin reads to gate merge/cleanup.
type outcomeHook struct{ onRun func(*HookContext) }

func (o *outcomeHook) Name() string    { return "outcome" }
func (o *outcomeHook) Phases() []Phase { return []Phase{PhaseRun} }
func (o *outcomeHook) Post(hc *HookContext, _ Phase) error {
	o.onRun(hc)
	return nil
}

// rewriteHook stands in for the setup/worktree hooks: a PreRun that consumes
// part of the request and replaces it with where the work landed.
type rewriteHook struct{ cwd string }

func (r *rewriteHook) Name() string { return "rewrite" }
func (r *rewriteHook) PreRun(hc *HookContext) error {
	hc.Request.Setup.Checkout = nil
	hc.Request.SetCwd(r.cwd)
	return nil
}

// A Post hook needs both halves: what the run was asked to do, and what it
// became. Without Original the first is simply gone by teardown time — which is
// why worktree.Plugin used to keep a private copy of its own effect.
func TestRunner_OriginalSurvivesAPreRunRewrite(t *testing.T) {
	var seen *HookContext
	r := &Runner[string]{
		Provider: &fakeProvider{events: func(int) []ai.Event {
			return []ai.Event{{Kind: ai.EventResult, Success: true}}
		}},
		Request: ai.Request{
			Prompt: api.Prompt{User: "go"},
			Setup:  &shell.Setup{Checkout: &shell.Checkout{Mode: shell.CheckoutRemote, URL: "https://example.com/x.git"}},
		},
		Hooks: []any{
			&rewriteHook{cwd: "/work/checkout"},
			&outcomeHook{onRun: func(hc *HookContext) { seen = hc }},
		},
	}

	_, err := r.Run(context.Background())
	require.NoError(t, err)
	require.NotNil(t, seen)

	require.NotNil(t, seen.Original.Setup.Checkout, "Original lost the checkout the run was asked to perform")
	assert.Equal(t, "https://example.com/x.git", seen.Original.Setup.Checkout.URL)
	assert.Nil(t, seen.Request.Setup.Checkout, "the current request should describe the result, not the request")
	assert.Equal(t, "/work/checkout", seen.Request.Cwd())
}

// isolatorHook is a hook that relocates the run, like worktree.Plugin.
type isolatorHook struct{ name string }

func (i *isolatorHook) Name() string                        { return i.name }
func (i *isolatorHook) IsolatesWorkspace(*HookContext) bool { return true }

func TestHookContext_EnsureSingleIsolator(t *testing.T) {
	hc := &HookContext{Hooks: []any{&isolatorHook{name: "setup"}}}
	assert.NoError(t, hc.EnsureSingleIsolator(), "one isolator is the normal case")

	hc.Hooks = append(hc.Hooks, &isolatorHook{name: "worktree"})
	err := hc.EnsureSingleIsolator()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setup")
	assert.Contains(t, err.Error(), "worktree")
}

// lifecycleHook appends every boundary it is dispatched to a shared log, as
// "<phase>" or, when the hook is named, "<name>:<phase>" — so several hooks can
// share one log and the test can read dispatch order off it. fail makes a chosen
// phase error; onTurn inspects the context the runner hands a turn dispatch.
type lifecycleHook struct {
	name   string
	log    *[]string
	fail   map[Phase]error
	onTurn func(*HookContext)
}

func (l *lifecycleHook) Name() string    { return l.name }
func (l *lifecycleHook) Phases() []Phase { return AllPhases() }

func (l *lifecycleHook) PreRun(*HookContext) error {
	l.record("preRun")
	return nil
}

func (l *lifecycleHook) Post(hc *HookContext, p Phase) error {
	l.record(string(p))
	if p == PhaseTurn && l.onTurn != nil {
		l.onTurn(hc)
	}
	return l.fail[p]
}

func (l *lifecycleHook) record(event string) {
	if l.name != "" {
		event = l.name + ":" + event
	}
	*l.log = append(*l.log, event)
}
