// ABOUTME: The regression test for api.Config.APIURL — it proves captain's API
// ABOUTME: backends actually call the endpoint they are told to, using no binaries.

package aimock_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/aimock"
	"github.com/flanksource/captain/pkg/aimock/anthropicmock"
	"github.com/flanksource/captain/pkg/aimock/openaimock"
	"github.com/flanksource/captain/pkg/api"

	// Registers the backend factories NewProvider looks up.
	_ "github.com/flanksource/captain/pkg/ai/provider"
)

const scenarioDir = "testdata/scenarios"

func loadScenario(t *testing.T, name string) *aimock.Scenario {
	t.Helper()
	scenario, err := aimock.Load(scenarioDir + "/" + name)
	require.NoError(t, err)
	return scenario
}

// mockedProvider builds the provider a caller would get from a Config whose
// APIURL points at a mock. The API key is set explicitly so no environment
// lookup can quietly supply a real credential.
func mockedProvider(t *testing.T, model api.Model, apiURL string) api.StreamingProvider {
	t.Helper()
	p, err := api.NewProvider(api.Config{Model: model, APIKey: aimock.DummyKey, APIURL: apiURL})
	require.NoError(t, err)
	streaming, ok := p.(api.StreamingProvider)
	require.True(t, ok, "%s provider must stream", model.Backend)
	return streaming
}

func drain(t *testing.T, events <-chan api.Event) (text string, result *api.Event) {
	t.Helper()
	var sb strings.Builder
	for ev := range events {
		switch ev.Kind {
		case api.EventText:
			sb.WriteString(ev.Text)
		case api.EventError:
			t.Fatalf("stream error: %s", ev.Error)
		case api.EventResult:
			copied := ev
			result = &copied
		}
	}
	return sb.String(), result
}

// servedPrompt returns the last user text the mock saw on path, failing when the
// generation never reached it — i.e. when the client called the real API instead.
func servedPrompt(t *testing.T, requests func() []aimock.Recorded, path string) string {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		served := requests()
		for i := len(served) - 1; i >= 0; i-- {
			if served[i].Path == path {
				require.Empty(t, served[i].Miss, "the mock answered %s with a miss", path)
				return served[i].Request.LastUserText()
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no request reached %s; the mock saw %+v", path, served)
			return ""
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const capitalPrompt = "What is the capital of France?"

const capitalAnswer = "The capital of France is Paris."

func TestAnthropicBackendHonoursAPIURL(t *testing.T) {
	srv, err := anthropicmock.Start(anthropicmock.Options{Scenario: loadScenario(t, "text-only.yaml")})
	require.NoError(t, err)
	t.Cleanup(srv.Close)

	model := api.Model{Name: "claude-sonnet-4-5", Backend: api.BackendAnthropic}
	events, err := mockedProvider(t, model, srv.APIURL()).
		ExecuteStream(context.Background(), api.Spec{Prompt: api.Prompt{User: capitalPrompt}})
	require.NoError(t, err)

	text, result := drain(t, events)
	assert.Equal(t, capitalAnswer, text)
	require.NotNil(t, result, "the stream must end with a result event")
	require.NotNil(t, result.Usage)
	assert.Equal(t, 14, result.Usage.InputTokens)
	assert.Equal(t, 8, result.Usage.OutputTokens)

	// The plugin lists models on init, so the generation is the last request, not
	// the only one.
	assert.Equal(t, capitalPrompt, servedPrompt(t, srv.Requests, "/v1/messages"))
	assert.Empty(t, srv.Remaining(), "the scenario must be played out")
}

func TestOpenAIBackendHonoursAPIURL(t *testing.T) {
	srv, err := openaimock.Start(openaimock.Options{Scenario: loadScenario(t, "text-only.yaml")})
	require.NoError(t, err)
	t.Cleanup(srv.Close)

	model := api.Model{Name: "gpt-5", Backend: api.BackendOpenAI}
	events, err := mockedProvider(t, model, srv.APIURL()).
		ExecuteStream(context.Background(), api.Spec{Prompt: api.Prompt{User: capitalPrompt}})
	require.NoError(t, err)

	text, result := drain(t, events)
	assert.Equal(t, capitalAnswer, text)
	require.NotNil(t, result, "the stream must end with a result event")

	assert.Equal(t, capitalPrompt, servedPrompt(t, srv.Requests, "/v1/responses"))
	assert.Empty(t, srv.Remaining(), "the scenario must be played out")
}

// A Config that cannot be honoured must fail rather than silently reach the real
// API — genkit's googlegenai plugin exposes no endpoint override.
func TestGeminiBackendRejectsAPIURL(t *testing.T) {
	_, err := api.NewProvider(api.Config{
		Model:  api.Model{Name: "gemini-2.5-pro", Backend: api.BackendGemini},
		APIKey: aimock.DummyKey,
		APIURL: "http://127.0.0.1:9999",
	})
	require.ErrorContains(t, err, "does not support an API URL override")
}
