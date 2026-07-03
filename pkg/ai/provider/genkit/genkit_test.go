package genkit

import (
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEffortConfig(t *testing.T) {
	tests := []struct {
		name    string
		backend ai.Backend
		req     ai.Request
		want    map[string]any
	}{
		{
			name:    "anthropic no effort defaults max_tokens only",
			backend: ai.BackendAnthropic,
			req:     ai.Request{},
			want:    map[string]any{"max_tokens": 4096},
		},
		{
			name:    "anthropic high effort adds thinking budget on top of base",
			backend: ai.BackendAnthropic,
			req:     ai.Request{Model: api.Model{Effort: api.EffortHigh}},
			want: map[string]any{
				"max_tokens": 24576 + 4096,
				"thinking":   map[string]any{"type": "enabled", "budget_tokens": 24576},
			},
		},
		{
			name:    "anthropic medium honours explicit max tokens as base",
			backend: ai.BackendAnthropic,
			req:     ai.Request{Model: api.Model{Effort: api.EffortMedium}, Budget: api.Budget{MaxTokens: 1000}},
			want: map[string]any{
				"max_tokens": 8192 + 1000,
				"thinking":   map[string]any{"type": "enabled", "budget_tokens": 8192},
			},
		},
		{
			name:    "openai high effort sets reasoning_effort",
			backend: ai.BackendOpenAI,
			req:     ai.Request{Model: api.Model{Effort: api.EffortHigh}},
			want:    map[string]any{"reasoning_effort": "high"},
		},
		{
			name:    "openai no effort omits config",
			backend: ai.BackendOpenAI,
			req:     ai.Request{},
			want:    nil,
		},
		{
			name:    "gemini low effort sets thinkingBudget",
			backend: ai.BackendGemini,
			req:     ai.Request{Model: api.Model{Effort: api.EffortLow}},
			want:    map[string]any{"thinkingConfig": map[string]any{"thinkingBudget": 2048}},
		},
		{
			name:    "gemini no effort omits config",
			backend: ai.BackendGemini,
			req:     ai.Request{},
			want:    nil,
		},
		{
			name:    "deepseek omits config (reasoning is selected by model)",
			backend: ai.BackendDeepSeek,
			req:     ai.Request{Model: api.Model{Effort: api.EffortHigh}},
			want:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, effortConfig(tt.backend, tt.req))
		})
	}
}

func TestMapUsage(t *testing.T) {
	got := mapUsage(&gkai.GenerationUsage{
		InputTokens:         120,
		OutputTokens:        45,
		ThoughtsTokens:      30,
		CachedContentTokens: 17,
		TotalTokens:         212,
	})

	assert.Equal(t, ai.Usage{
		InputTokens:     120,
		OutputTokens:    45,
		ReasoningTokens: 30, // ThoughtsTokens -> ReasoningTokens
		CacheReadTokens: 17, // CachedContentTokens -> CacheReadTokens
	}, got)

	assert.Equal(t, ai.Usage{}, mapUsage(nil))
}

func TestModelRef(t *testing.T) {
	tests := []struct {
		name    string
		backend ai.Backend
		model   string
		want    string
		wantErr bool
	}{
		{"anthropic bare", ai.BackendAnthropic, "claude-sonnet-4", "anthropic/claude-sonnet-4", false},
		{"openai bare", ai.BackendOpenAI, "gpt-4o", "openai/gpt-4o", false},
		{"gemini bare", ai.BackendGemini, "gemini-2.5-pro", "googleai/gemini-2.5-pro", false},
		{"gemini normalizes models prefix", ai.BackendGemini, "models/gemini-2.5-pro", "googleai/gemini-2.5-pro", false},
		{"deepseek bare", ai.BackendDeepSeek, "deepseek-chat", "deepseek/deepseek-chat", false},
		{"deepseek re-prefixes existing", ai.BackendDeepSeek, "deepseek/deepseek-reasoner", "deepseek/deepseek-reasoner", false},
		{"anthropic re-prefixes existing", ai.BackendAnthropic, "anthropic/claude-opus-4", "anthropic/claude-opus-4", false},
		{"unsupported backend errors", ai.BackendClaudeCLI, "claude-code-foo", "", true},
		{"empty model errors", ai.BackendAnthropic, "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := modelRef(tt.backend, tt.model)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPricingModelID(t *testing.T) {
	assert.Equal(t, "anthropic/claude-sonnet-4", pricingModelID(ai.BackendAnthropic, "claude-sonnet-4"))
	assert.Equal(t, "openai/gpt-4o", pricingModelID(ai.BackendOpenAI, "gpt-4o"))
	// Gemini's OpenRouter pricing key is google/, not the genkit googleai/ ref.
	assert.Equal(t, "google/gemini-2.5-pro", pricingModelID(ai.BackendGemini, "googleai/gemini-2.5-pro"))
	assert.Equal(t, "deepseek/deepseek-chat", pricingModelID(ai.BackendDeepSeek, "deepseek/deepseek-chat"))
}

func TestNewMissingAPIKey(t *testing.T) {
	// Ensure no provider key leaks in from the environment.
	for _, env := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "DEEPSEEK_API_KEY"} {
		t.Setenv(env, "")
	}

	for _, backend := range []ai.Backend{ai.BackendAnthropic, ai.BackendOpenAI, ai.BackendGemini, ai.BackendDeepSeek} {
		t.Run(string(backend), func(t *testing.T) {
			_, err := New(ai.Config{Model: api.Model{Backend: backend, Name: "some-model"}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no API key")
		})
	}
}

func TestNewUnsupportedBackend(t *testing.T) {
	_, err := New(ai.Config{Model: api.Model{Backend: ai.BackendClaudeCLI, Name: "claude-code-foo"}, APIKey: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support backend")
}
