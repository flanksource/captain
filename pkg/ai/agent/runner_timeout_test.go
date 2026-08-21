package agent

import (
	"context"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// hangingProvider holds its event stream open until the context is done — the
// shape of a wedged agent turn (a supervised provider process that stops
// answering, but never exits).
type hangingProvider struct{}

func (h *hangingProvider) GetModel() string       { return "hanging" }
func (h *hangingProvider) GetBackend() ai.Backend { return ai.Backend("fake") }

func (h *hangingProvider) Execute(ctx context.Context, _ ai.Request) (*ai.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func (h *hangingProvider) ExecuteStream(ctx context.Context, _ ai.Request) (<-chan ai.Event, error) {
	ch := make(chan ai.Event)
	go func() {
		defer close(ch)
		<-ctx.Done()
	}()
	return ch, nil
}

// runWithin runs r and reports whether it returned inside limit. The run's
// context is cancelled on cleanup so a deliberately unbounded case does not
// leave the provider goroutine parked for the rest of the suite.
func runWithin(t *testing.T, r *Runner[string], limit time.Duration) (Result[string], error, bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	type outcome struct {
		res Result[string]
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		res, err := r.Run(ctx)
		done <- outcome{res, err}
	}()
	select {
	case o := <-done:
		return o.res, o.err, true
	case <-time.After(limit):
		return Result[string]{}, nil, false
	}
}

// TestRunner_BudgetTimeoutBoundsTheRun is the backstop for the reported
// `gavel pr status --ai-fix` hang: the prompt declared budget.timeout 45m, the
// request carried it, and nothing enforced it because only pkg/cli converted it
// to a deadline. A caller driving the Runner directly ran unbounded.
func TestRunner_BudgetTimeoutBoundsTheRun(t *testing.T) {
	r := &Runner[string]{
		Provider: &hangingProvider{},
		Request: ai.Request{
			Prompt: api.Prompt{User: "go"},
			Budget: api.Budget{Timeout: "150ms"},
		},
	}

	_, _, returned := runWithin(t, r, 10*time.Second)
	assert.True(t, returned, "Run ignored the declared budget.timeout and hung")
}

// TestRunner_NoBudgetTimeoutStaysUnbounded guards the opt-in: an undeclared
// timeout must not invent a deadline that truncates a long legitimate run.
func TestRunner_NoBudgetTimeoutStaysUnbounded(t *testing.T) {
	r := &Runner[string]{
		Provider: &hangingProvider{},
		Request:  ai.Request{Prompt: api.Prompt{User: "go"}},
	}

	_, _, returned := runWithin(t, r, 300*time.Millisecond)
	assert.False(t, returned, "Run applied a deadline that no budget declared")
}

// TestRunner_RejectsUnparseableBudgetTimeout keeps a bad ceiling loud rather
// than silently falling back to a default the caller never asked for.
func TestRunner_RejectsUnparseableBudgetTimeout(t *testing.T) {
	r := &Runner[string]{
		Provider: &fakeProvider{events: func(int) []ai.Event {
			return []ai.Event{{Kind: ai.EventResult, Success: true}}
		}},
		Request: ai.Request{
			Prompt: api.Prompt{User: "go"},
			Budget: api.Budget{Timeout: "45minutes"},
		},
	}

	_, err := r.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "45minutes")
}
