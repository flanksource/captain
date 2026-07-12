package ai

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type modelErrorTestProvider struct {
	backend Backend
	model   string
	err     error
	events  []Event
}

func (p *modelErrorTestProvider) GetModel() string    { return p.model }
func (p *modelErrorTestProvider) GetBackend() Backend { return p.backend }
func (p *modelErrorTestProvider) Execute(context.Context, Request) (*Response, error) {
	return nil, p.err
}
func (p *modelErrorTestProvider) ExecuteStream(context.Context, Request) (<-chan Event, error) {
	if p.err != nil {
		return nil, p.err
	}
	ch := make(chan Event, len(p.events))
	for _, ev := range p.events {
		ch <- ev
	}
	close(ch)
	return ch, nil
}

func stubModelRecommendations(t *testing.T) {
	t.Helper()
	previous := modelAvailabilityResolver
	modelAvailabilityResolver = func(context.Context, Backend) []ModelDef {
		return []ModelDef{
			{ID: "gpt-5.6-sol"},
			{ID: "gpt-5.6-terra"},
			{ID: "gpt-5.6-luna"},
			{ID: "gpt-5.5"},
			{ID: "gpt-5.4"},
			{ID: "gpt-5.4-mini"},
		}
	}
	t.Cleanup(func() { modelAvailabilityResolver = previous })
}

func TestRecommendModelErrorUsesAvailableBackendModels(t *testing.T) {
	stubModelRecommendations(t)
	base := fmtModelUnavailable("The 'gpt-5.6-line' model is not supported when using Codex with a ChatGPT account.")
	err := recommendModelError(context.Background(), BackendCodexAgent, "gpt-5.6-line", base)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `did you mean "gpt-5.6-luna"?`)
	assert.Contains(t, err.Error(), "available models for codex-agent: gpt-5.6-sol, gpt-5.6-terra, gpt-5.6-luna, gpt-5.5, gpt-5.4 (+1 more)")
	assert.ErrorIs(t, err, ErrModelUnavailable)
}

func TestModelErrorProviderEnrichesBufferedFailure(t *testing.T) {
	stubModelRecommendations(t)
	base := &modelErrorTestProvider{
		backend: BackendCodexCLI,
		model:   "gpt-5.6-line",
		err:     errors.New("unsupported model gpt-5.6-line"),
	}
	p := withModelErrorRecommendations(base)
	_, err := p.Execute(context.Background(), api.Spec{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `did you mean "gpt-5.6-luna"?`)
}

func TestModelErrorProviderEnrichesStreamingFailure(t *testing.T) {
	previous := modelAvailabilityResolver
	resolveCalls := 0
	modelAvailabilityResolver = func(context.Context, Backend) []ModelDef {
		resolveCalls++
		return []ModelDef{{ID: "gpt-5.6-luna"}, {ID: "gpt-5.6-sol"}}
	}
	t.Cleanup(func() { modelAvailabilityResolver = previous })
	base := &modelErrorTestProvider{
		backend: BackendCodexAgent,
		model:   "gpt-5.6-line",
		events: []Event{
			{Kind: EventError, Error: "unknown model gpt-5.6-line"},
			{Kind: EventError, Error: "unknown model gpt-5.6-line"},
		},
	}
	p := withModelErrorRecommendations(base).(StreamingProvider)
	ch, err := p.ExecuteStream(context.Background(), api.Spec{})
	require.NoError(t, err)
	events := drain(ch)
	require.Len(t, events, 2)
	assert.Contains(t, events[0].Error, `did you mean "gpt-5.6-luna"?`)
	assert.Contains(t, events[1].Error, `did you mean "gpt-5.6-luna"?`)
	assert.Equal(t, 1, resolveCalls, "repeated terminal events should reuse one availability snapshot")
}

func TestRecommendModelErrorLeavesOtherFailuresUnchanged(t *testing.T) {
	stubModelRecommendations(t)
	base := errors.New("authentication failed: invalid API key")
	err := recommendModelError(context.Background(), BackendOpenAI, "gpt-5.6", base)
	assert.Same(t, base, err)
	assert.False(t, strings.Contains(err.Error(), "available models"))
}

func fmtModelUnavailable(message string) error {
	return fmt.Errorf("%w: %s", ErrModelUnavailable, message)
}
