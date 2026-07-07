package agent

import (
	"context"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProvider emits a scripted set of events per iteration.
type fakeProvider struct {
	events func(iter int) []ai.Event
	calls  int
}

func (f *fakeProvider) GetModel() string       { return "fake" }
func (f *fakeProvider) GetBackend() ai.Backend { return ai.Backend("fake") }
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

func TestRunner_PreRunPostRunOrder(t *testing.T) {
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
	// PreRun before the loop; PostRun after.
	assert.Equal(t, []string{"preRun", "postRun"}, log)
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

func (v verifyHook) Name() string                              { return v.name }
func (v verifyHook) Verify(hc *HookContext) (VerifyResult, error) { return v.fn(hc) }

type lifecycleHook struct{ log *[]string }

func (l *lifecycleHook) Name() string                { return "lifecycle" }
func (l *lifecycleHook) PreRun(*HookContext) error   { *l.log = append(*l.log, "preRun"); return nil }
func (l *lifecycleHook) PostRun(*HookContext) error  { *l.log = append(*l.log, "postRun"); return nil }
