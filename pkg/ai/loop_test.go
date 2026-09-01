package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

// fakeStreamingProvider replays a scripted sequence of event slices, one per
// ExecuteStream invocation. It records every Request it receives so tests can
// assert SessionReuse forwarding.
type fakeStreamingProvider struct {
	scripts  [][]Event
	requests []Request
	err      error
	runtime  Runtime
	model    string
}

func (f *fakeStreamingProvider) GetModel() string {
	if f.model != "" {
		return f.model
	}
	return "fake"
}
func (f *fakeStreamingProvider) GetRuntime() Runtime {
	if (f.runtime != Runtime{}) {
		return f.runtime
	}
	return RuntimeOf(Anthropic, ModeAgent)
}
func (f *fakeStreamingProvider) Execute(ctx context.Context, req Request) (*Response, error) {
	return nil, errors.New("not used")
}
func (f *fakeStreamingProvider) ExecuteStream(ctx context.Context, req Request) (<-chan Event, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return nil, f.err
	}
	idx := len(f.requests) - 1
	if idx >= len(f.scripts) {
		ch := make(chan Event)
		close(ch)
		return ch, nil
	}
	script := f.scripts[idx]
	ch := make(chan Event, len(script))
	for _, ev := range script {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func resultEvent(cost float64, success bool) Event {
	return Event{Kind: EventResult, CostUSD: cost, Success: success}
}

func systemEvent(sid string) Event {
	return Event{Kind: EventSystem, SessionID: sid}
}

func TestRunUntil_ConditionMetOnFirstIteration(t *testing.T) {
	p := &fakeStreamingProvider{}
	res, err := RunUntil(context.Background(), LoopOptions{
		Provider:     p,
		BuildRequest: func(iter int, prev *LoopIteration) (Request, bool) { return Request{}, false },
	})
	if err != nil {
		t.Fatalf("RunUntil err: %v", err)
	}
	if res.StopReason != "condition-met" {
		t.Errorf("StopReason = %q, want condition-met", res.StopReason)
	}
	if len(p.requests) != 0 {
		t.Errorf("expected provider not invoked, got %d calls", len(p.requests))
	}
}

func TestRunUntil_StopsWhenConditionMetAfterFirstRun(t *testing.T) {
	p := &fakeStreamingProvider{
		scripts: [][]Event{
			{systemEvent("sess-1"), Event{Kind: EventText, Text: "fixed"}, resultEvent(0.01, true)},
		},
	}
	res, err := RunUntil(context.Background(), LoopOptions{
		Provider: p,
		BuildRequest: func(iter int, prev *LoopIteration) (Request, bool) {
			if iter == 0 {
				return Request{Prompt: api.Prompt{User: "first"}}, true
			}
			return Request{}, false
		},
	})
	if err != nil {
		t.Fatalf("RunUntil err: %v", err)
	}
	if res.StopReason != "condition-met" {
		t.Errorf("StopReason = %q, want condition-met", res.StopReason)
	}
	if len(res.Iterations) != 1 {
		t.Fatalf("Iterations = %d, want 1", len(res.Iterations))
	}
	if !res.Iterations[0].Success {
		t.Error("iteration not marked Success despite EventResult{Success:true}")
	}
	if res.TotalCost != 0.01 {
		t.Errorf("TotalCost = %v, want 0.01", res.TotalCost)
	}
}

func TestRunUntil_HitsMaxIterations(t *testing.T) {
	mkScript := func() []Event { return []Event{resultEvent(0.001, true)} }
	p := &fakeStreamingProvider{scripts: [][]Event{mkScript(), mkScript(), mkScript(), mkScript()}}
	res, err := RunUntil(context.Background(), LoopOptions{
		Provider:      p,
		MaxIterations: 2,
		BuildRequest: func(iter int, prev *LoopIteration) (Request, bool) {
			return Request{Prompt: api.Prompt{User: "loop"}}, true
		},
	})
	if err != nil {
		t.Fatalf("RunUntil err: %v", err)
	}
	if res.StopReason != "max-iterations" {
		t.Errorf("StopReason = %q, want max-iterations", res.StopReason)
	}
	if len(res.Iterations) != 2 {
		t.Errorf("Iterations = %d, want 2", len(res.Iterations))
	}
}

// A provider that reports usage but no cost (codex-cli/cmux, gemini-cli) must
// still accrue a real cost so MaxCostUSD can enforce a budget for it (finding
// C2). The loop prices the usage from the registry.
func TestRunUntil_PricesUsageWhenProviderReportsNoCost(t *testing.T) {
	p := &fakeStreamingProvider{
		runtime: RuntimeOf(Anthropic, ModeAPI),
		model:   "claude-sonnet-4",
		scripts: [][]Event{{
			{Kind: EventResult, Success: true, CostUSD: 0, Usage: &Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}},
		}},
	}
	res, err := RunUntil(context.Background(), LoopOptions{
		Provider:      p,
		MaxIterations: 1,
		BuildRequest: func(iter int, prev *LoopIteration) (Request, bool) {
			if iter > 0 {
				return Request{}, false
			}
			return Request{Prompt: api.Prompt{User: "loop"}}, true
		},
	})
	if err != nil {
		t.Fatalf("RunUntil err: %v", err)
	}
	if res.TotalCost <= 0 {
		t.Fatalf("TotalCost = %v, want > 0 (loop must price usage the provider left uncosted)", res.TotalCost)
	}
}

func TestRunUntil_HitsMaxCost(t *testing.T) {
	p := &fakeStreamingProvider{
		scripts: [][]Event{
			{resultEvent(0.05, true)},
			{resultEvent(0.05, true)},
			{resultEvent(0.05, true)},
		},
	}
	res, err := RunUntil(context.Background(), LoopOptions{
		Provider:      p,
		MaxIterations: 10,
		MaxCostUSD:    0.07,
		BuildRequest: func(iter int, prev *LoopIteration) (Request, bool) {
			return Request{Prompt: api.Prompt{User: "loop"}}, true
		},
	})
	if err != nil {
		t.Fatalf("RunUntil err: %v", err)
	}
	if res.StopReason != "max-cost" {
		t.Errorf("StopReason = %q, want max-cost", res.StopReason)
	}
	// Cap is enforced after-the-fact, before the *next* call. Iter 0 costs
	// 0.05 (under cap, runs). Iter 1 costs another 0.05 (cumulative 0.10
	// crosses cap mid-run, but the loop only checks pre-call). Iter 2 is
	// pre-empted because cumulative (0.10) ≥ cap (0.07). So we expect 2 runs.
	if len(res.Iterations) != 2 {
		t.Errorf("Iterations = %d, want 2", len(res.Iterations))
	}
	if res.TotalCost < 0.07 {
		t.Errorf("TotalCost = %v, want at least 0.07 (cap)", res.TotalCost)
	}
}

func TestRunUntil_SessionReuse(t *testing.T) {
	p := &fakeStreamingProvider{
		scripts: [][]Event{
			{systemEvent("sess-A"), resultEvent(0.01, true)},
			{systemEvent("sess-A"), resultEvent(0.01, true)},
		},
	}
	_, err := RunUntil(context.Background(), LoopOptions{
		Provider:      p,
		MaxIterations: 2,
		SessionReuse:  true,
		BuildRequest: func(iter int, prev *LoopIteration) (Request, bool) {
			return Request{Prompt: api.Prompt{User: "loop"}}, true
		},
	})
	if err != nil {
		t.Fatalf("RunUntil err: %v", err)
	}
	if len(p.requests) != 2 {
		t.Fatalf("expected 2 provider calls, got %d", len(p.requests))
	}
	if p.requests[0].SessionID != "" {
		t.Errorf("first request SessionID = %q, want empty (no prior session)", p.requests[0].SessionID)
	}
	if p.requests[1].SessionID != "sess-A" {
		t.Errorf("second request SessionID = %q, want sess-A (forwarded)", p.requests[1].SessionID)
	}
}

func TestRunUntil_PropagatesProviderError(t *testing.T) {
	wantErr := errors.New("boom")
	p := &fakeStreamingProvider{err: wantErr}
	res, err := RunUntil(context.Background(), LoopOptions{
		Provider:     p,
		BuildRequest: func(iter int, prev *LoopIteration) (Request, bool) { return Request{}, true },
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want wraps %v", err, wantErr)
	}
	if res.StopReason != "error" {
		t.Errorf("StopReason = %q, want error", res.StopReason)
	}
}
