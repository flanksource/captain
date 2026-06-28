package agent

import (
	"context"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
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

	r := &Runner{
		Provider: prov,
		Repo:     "/repo",
		Build: func(_ *RunContext, _ int, _ *ai.LoopIteration, _ string) ai.Request {
			return ai.Request{Prompt: "go"}
		},
	}
	res, err := r.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "sess-1", res.SessionID)
	assert.Equal(t, []string{"pkg/a.go", "pkg/b.go"}, res.ChangedFiles)
	assert.Equal(t, 1, prov.calls, "no verifiers ⇒ exactly one iteration")
}

func TestRunner_VerifyDrivesRerunThenStops(t *testing.T) {
	prov := &fakeProvider{events: func(int) []ai.Event {
		return []ai.Event{{Kind: ai.EventResult, Success: true}}
	}}

	var verifyCalls int
	var feedbackSeen []string

	r := &Runner{
		Provider: prov,
		Scope:    ScopeAll,
		Plugins: []Plugin{
			verifyPlugin{name: "lint", fn: func(*RunContext, *ai.LoopIteration) (Verdict, error) {
				verifyCalls++
				if verifyCalls == 1 {
					return Verdict{OK: false, Reason: "1 violation", Feedback: "fix line 7"}, nil
				}
				return Verdict{OK: true}, nil
			}},
		},
		Build: func(_ *RunContext, _ int, _ *ai.LoopIteration, feedback string) ai.Request {
			feedbackSeen = append(feedbackSeen, feedback)
			return ai.Request{Prompt: "go"}
		},
	}

	res, err := r.Run(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 2, prov.calls, "should re-run once after the first failing verify")
	assert.Equal(t, 2, verifyCalls)
	require.Len(t, res.Verdicts, 2)
	assert.False(t, res.Verdicts[0].OK)
	assert.True(t, res.Verdicts[1].OK)
	// iter0 has no feedback; iter1 carries the failing verdict's feedback.
	assert.Equal(t, []string{"", "fix line 7"}, feedbackSeen)
}

func TestRunner_SetupFinalizeTeardownOrder(t *testing.T) {
	prov := &fakeProvider{events: func(int) []ai.Event {
		return []ai.Event{{Kind: ai.EventResult, Success: true}}
	}}

	var log []string
	plugin := &lifecyclePlugin{log: &log}

	r := &Runner{
		Provider: prov,
		Plugins:  []Plugin{plugin},
		Build: func(*RunContext, int, *ai.LoopIteration, string) ai.Request {
			log = append(log, "build")
			return ai.Request{}
		},
	}
	_, err := r.Run(context.Background())
	require.NoError(t, err)
	// setup before the loop; finalize after; teardown last.
	assert.Equal(t, []string{"setup", "build", "finalize", "teardown"}, log)
}

// --- test plugins -----------------------------------------------------------

type verifyPlugin struct {
	name string
	fn   func(*RunContext, *ai.LoopIteration) (Verdict, error)
}

func (v verifyPlugin) Name() string { return v.name }
func (v verifyPlugin) Verify(rc *RunContext, it *ai.LoopIteration) (Verdict, error) {
	return v.fn(rc, it)
}

type lifecyclePlugin struct{ log *[]string }

func (l *lifecyclePlugin) Name() string { return "lifecycle" }
func (l *lifecyclePlugin) Setup(*RunContext) (func() error, error) {
	*l.log = append(*l.log, "setup")
	return func() error { *l.log = append(*l.log, "teardown"); return nil }, nil
}
func (l *lifecyclePlugin) Finalize(*RunContext, *RunResult) error {
	*l.log = append(*l.log, "finalize")
	return nil
}
