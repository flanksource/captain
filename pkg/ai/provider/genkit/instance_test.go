package genkit

import (
	"testing"

	"github.com/firebase/genkit/go/plugins/anthropic"
	"github.com/firebase/genkit/go/plugins/compat_oai"
	"github.com/firebase/genkit/go/plugins/compat_oai/openai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/ai"
)

const mockBaseURL = "http://127.0.0.1:9999"

// The endpoint has to be part of the cache key: without it a mock-pointed
// instance would be served from the entry built against the real API, making the
// override silently order-dependent.
func TestInstanceKeyDistinguishesBaseURL(t *testing.T) {
	real := instanceKey{backend: ai.BackendAnthropic, apiKey: "k"}
	mocked := instanceKey{backend: ai.BackendAnthropic, apiKey: "k", baseURL: mockBaseURL}
	assert.NotEqual(t, real, mocked)
}

func TestPluginForAnthropicCarriesBaseURL(t *testing.T) {
	plugin, err := pluginFor(ai.BackendAnthropic, "k", mockBaseURL)
	require.NoError(t, err)

	got, ok := plugin.(*anthropic.Anthropic)
	require.True(t, ok)
	assert.Equal(t, mockBaseURL, got.BaseURL)
}

// openai.OpenAI has no BaseURL field, so the override has to travel as a client
// request option instead.
func TestPluginForOpenAICarriesBaseURLAsAnOption(t *testing.T) {
	plugin, err := pluginFor(ai.BackendOpenAI, "k", mockBaseURL)
	require.NoError(t, err)
	overridden, ok := plugin.(*openai.OpenAI)
	require.True(t, ok)
	assert.Len(t, overridden.Opts, 1, "an overridden endpoint adds exactly one request option")

	plugin, err = pluginFor(ai.BackendOpenAI, "k", "")
	require.NoError(t, err)
	plain, ok := plugin.(*openai.OpenAI)
	require.True(t, ok)
	assert.Empty(t, plain.Opts, "no override must leave the client untouched")
}

func TestPluginForDeepSeekOverridesItsHardcodedEndpoint(t *testing.T) {
	plugin, err := pluginFor(ai.BackendDeepSeek, "k", mockBaseURL)
	require.NoError(t, err)
	overridden, ok := plugin.(*compat_oai.OpenAICompatible)
	require.True(t, ok)
	assert.Equal(t, mockBaseURL, overridden.BaseURL)

	plugin, err = pluginFor(ai.BackendDeepSeek, "k", "")
	require.NoError(t, err)
	plain, ok := plugin.(*compat_oai.OpenAICompatible)
	require.True(t, ok)
	assert.Equal(t, deepSeekBaseURL, plain.BaseURL)
}

// googlegenai exposes no endpoint override, so an APIURL that cannot be honoured
// must fail rather than quietly calling the real API.
func TestPluginForRejectsGeminiBaseURL(t *testing.T) {
	_, err := pluginFor(ai.BackendGemini, "k", mockBaseURL)
	require.ErrorContains(t, err, "does not support an API URL override")

	_, err = pluginFor(ai.BackendGemini, "k", "")
	require.NoError(t, err)
}

func TestGetInstanceRequiresAnAPIKey(t *testing.T) {
	_, err := getInstance(t.Context(), ai.BackendAnthropic, "", "")
	require.ErrorIs(t, err, ai.ErrNoAPIKey)
}
