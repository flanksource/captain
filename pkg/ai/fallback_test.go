package ai

import (
	"context"
	"errors"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeProv is a scriptable Provider/StreamingProvider used to drive the fallback
// provider's advance logic without a real backend.
type fakeProv struct {
	model        string
	execResp     *Response
	execErr      error
	execCalls    int
	streamEvents []Event
	streamErr    error
	streamCalls  int
}

func (f *fakeProv) GetModel() string    { return f.model }
func (f *fakeProv) GetBackend() Backend { return BackendAnthropic }

func (f *fakeProv) Execute(_ context.Context, _ Request) (*Response, error) {
	f.execCalls++
	if f.execErr != nil {
		return nil, f.execErr
	}
	return f.execResp, nil
}

func (f *fakeProv) ExecuteStream(_ context.Context, _ Request) (<-chan Event, error) {
	f.streamCalls++
	if f.streamErr != nil {
		return nil, f.streamErr
	}
	ch := make(chan Event)
	go func() {
		defer close(ch)
		for _, ev := range f.streamEvents {
			ch <- ev
		}
	}()
	return ch, nil
}

// buildFrom returns a builder that resolves each candidate config to a scripted
// provider by model name; a missing name yields a construction error.
func buildFrom(provs map[string]*fakeProv) func(Config) (Provider, error) {
	return func(cfg Config) (Provider, error) {
		p, ok := provs[cfg.Model.Name]
		if !ok {
			return nil, errors.New("no provider for " + cfg.Model.Name)
		}
		return p, nil
	}
}

func newTestFallback(candidates []string, provs map[string]*fakeProv) *fallbackProvider {
	models := make([]api.Model, len(candidates))
	for i, name := range candidates {
		models[i] = api.Model{Name: name}
	}
	return &fallbackProvider{
		candidates: models,
		build:      buildFrom(provs),
		built:      make([]Provider, len(models)),
	}
}

func drain(ch <-chan Event) []Event {
	var out []Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestFallback_Execute_RetryableAdvances(t *testing.T) {
	primary := &fakeProv{model: "primary", execErr: errors.New("openai: 429 rate limit")}
	fallback := &fakeProv{model: "fallback", execResp: &Response{Text: "ok", Model: "fallback"}}
	fp := newTestFallback([]string{"primary", "fallback"}, map[string]*fakeProv{"primary": primary, "fallback": fallback})

	resp, err := fp.Execute(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Text)
	assert.Equal(t, "fallback", fp.GetModel())
	assert.Equal(t, 1, primary.execCalls)
	assert.Equal(t, 1, fallback.execCalls)
}

func TestFallback_Execute_NonRetryableStops(t *testing.T) {
	primary := &fakeProv{model: "primary", execErr: errors.New("invalid request: missing user")}
	fallback := &fakeProv{model: "fallback", execResp: &Response{Text: "ok"}}
	fp := newTestFallback([]string{"primary", "fallback"}, map[string]*fakeProv{"primary": primary, "fallback": fallback})

	_, err := fp.Execute(context.Background(), Request{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid request")
	assert.Equal(t, 0, fallback.execCalls, "non-retryable error must not advance to the fallback")
}

func TestFallback_Execute_BuildFailureAdvances(t *testing.T) {
	fallback := &fakeProv{model: "fallback", execResp: &Response{Text: "ok"}}
	// "primary" is absent from the map, so its provider fails to build.
	fp := newTestFallback([]string{"primary", "fallback"}, map[string]*fakeProv{"fallback": fallback})

	resp, err := fp.Execute(context.Background(), Request{})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Text)
	assert.Equal(t, 1, fallback.execCalls)
}

func TestFallback_Execute_AllFailReturnsPrimaryError(t *testing.T) {
	primary := &fakeProv{model: "primary", execErr: errors.New("primary 429 overloaded")}
	fallback := &fakeProv{model: "fallback", execErr: errors.New("fallback 503 overloaded")}
	fp := newTestFallback([]string{"primary", "fallback"}, map[string]*fakeProv{"primary": primary, "fallback": fallback})

	_, err := fp.Execute(context.Background(), Request{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "primary 429", "the primary's error is the most actionable")
}

func TestFallback_Stream_PreContentAdvances(t *testing.T) {
	primary := &fakeProv{model: "primary", streamEvents: []Event{
		{Kind: EventSystem, SessionID: "primary-session"},
		{Kind: EventError, Error: "429 overloaded"},
	}}
	fallback := &fakeProv{model: "fallback", streamEvents: []Event{
		{Kind: EventText, Text: "hello"},
		{Kind: EventResult, Success: true},
	}}
	fp := newTestFallback([]string{"primary", "fallback"}, map[string]*fakeProv{"primary": primary, "fallback": fallback})

	ch, err := fp.ExecuteStream(context.Background(), Request{})
	require.NoError(t, err)
	events := drain(ch)

	require.Len(t, events, 2)
	assert.Equal(t, EventText, events[0].Kind)
	assert.Equal(t, "hello", events[0].Text)
	assert.Equal(t, EventResult, events[1].Kind)
	for _, ev := range events {
		assert.NotEqual(t, "primary-session", ev.SessionID, "discarded candidate's session must not leak")
		assert.NotEqual(t, EventError, ev.Kind, "pre-content error must not surface once a fallback succeeds")
	}
	assert.Equal(t, 1, fallback.streamCalls)
}

func TestFallback_Stream_CommittedErrorSurfaces(t *testing.T) {
	primary := &fakeProv{model: "primary", streamEvents: []Event{
		{Kind: EventText, Text: "partial"},
		{Kind: EventError, Error: "429 mid-stream"},
	}}
	fallback := &fakeProv{model: "fallback", streamEvents: []Event{{Kind: EventText, Text: "should-not-run"}}}
	fp := newTestFallback([]string{"primary", "fallback"}, map[string]*fakeProv{"primary": primary, "fallback": fallback})

	ch, err := fp.ExecuteStream(context.Background(), Request{})
	require.NoError(t, err)
	events := drain(ch)

	require.Len(t, events, 2)
	assert.Equal(t, "partial", events[0].Text)
	assert.Equal(t, EventError, events[1].Kind)
	assert.Equal(t, "429 mid-stream", events[1].Error)
	assert.Equal(t, 0, fallback.streamCalls, "a committed stream must not fall back after emitting content")
}
