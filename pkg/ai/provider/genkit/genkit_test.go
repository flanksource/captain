package genkit

import (
	"fmt"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	gkai "github.com/firebase/genkit/go/ai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMapUsage(t *testing.T) {
	// genkit reports overlapping buckets differently per backend; mapUsage must
	// normalize to captain's disjoint contract (Input excludes cache, Output
	// excludes reasoning).
	raw := &gkai.GenerationUsage{
		InputTokens:         120,
		OutputTokens:        45,
		ThoughtsTokens:      30,
		CachedContentTokens: 17,
		TotalTokens:         212,
	}

	// Anthropic: input_tokens already excludes cache and there is no reasoning
	// fold, so both buckets pass through unchanged.
	assert.Equal(t, ai.Usage{
		InputTokens:     120,
		OutputTokens:    45,
		ReasoningTokens: 30,
		CacheReadTokens: 17,
	}, mapUsage(raw, ai.BackendAnthropic))

	// Gemini: PromptTokenCount folds in cache → net input; CandidatesTokenCount
	// excludes thoughts → output passes through.
	assert.Equal(t, ai.Usage{
		InputTokens:     103, // 120 - 17
		OutputTokens:    45,
		ReasoningTokens: 30,
		CacheReadTokens: 17,
	}, mapUsage(raw, ai.BackendGemini))

	// OpenAI/DeepSeek (compat_oai): prompt_tokens folds in cache AND
	// completion_tokens folds in reasoning → net both.
	openaiWant := ai.Usage{
		InputTokens:     103, // 120 - 17
		OutputTokens:    15,  // 45 - 30
		ReasoningTokens: 30,
		CacheReadTokens: 17,
	}
	assert.Equal(t, openaiWant, mapUsage(raw, ai.BackendOpenAI))
	assert.Equal(t, openaiWant, mapUsage(raw, ai.BackendDeepSeek))

	// Disjoint invariant: for cache-folding backends InputTokens no longer
	// overlaps CacheReadTokens, so pricing cannot bill the cached prefix twice.
	for _, backend := range []ai.Backend{ai.BackendGemini, ai.BackendOpenAI, ai.BackendDeepSeek} {
		got := mapUsage(raw, backend)
		assert.Equal(t, raw.InputTokens, got.InputTokens+got.CacheReadTokens, "backend %s input+cache", backend)
	}

	assert.Equal(t, ai.Usage{}, mapUsage(nil, ai.BackendAnthropic))
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

func TestNewNormalizesBackendModelName(t *testing.T) {
	p, err := New(ai.Config{
		Model:  api.Model{Backend: ai.BackendAnthropic, Name: "opus-4-8"},
		APIKey: "test-anthropic-key",
	})
	require.NoError(t, err)

	assert.Equal(t, "claude-opus-4-8", p.GetModel())
	assert.Equal(t, "anthropic/claude-opus-4-8", p.modelRef)
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
			assert.ErrorIs(t, err, ai.ErrNoAPIKey)
		})
	}
}

func TestNewUnsupportedBackend(t *testing.T) {
	_, err := New(ai.Config{Model: api.Model{Backend: ai.BackendClaudeCLI, Name: "claude-code-foo"}, APIKey: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support backend")
}

func TestIsSchemaMismatch(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "genkit constrained-output rejection",
			err:  fmt.Errorf("model failed to generate output matching expected schema: data did not match expected schema:\n- title: String length must be less than or equal to 40"),
			want: true,
		},
		{
			name: "genkit inner detail phrasing",
			err:  fmt.Errorf("data did not match expected schema:\n- branch: String length must be less than or equal to 40"),
			want: true,
		},
		{
			name: "unrelated transport error",
			err:  fmt.Errorf("dial tcp: connection refused"),
			want: false,
		},
		{
			name: "rate-limit error is not a schema mismatch",
			err:  fmt.Errorf("429 rate limit exceeded"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, isSchemaMismatch(tt.err))
		})
	}
}

func TestBackendOutputSchema_NormalizesOpenAIStructuredOutput(t *testing.T) {
	type answer struct {
		Answer string `json:"answer"`
		Detail string `json:"detail,omitempty"`
	}
	req := ai.Request{Prompt: api.Prompt{Schema: &answer{}}}

	schema, handled, err := backendOutputSchema(ai.BackendOpenAI, req)
	require.NoError(t, err)
	require.True(t, handled)
	assert.Equal(t, []any{"answer", "detail"}, schema["required"])
	assert.Equal(t, false, schema["additionalProperties"])

	_, handled, err = backendOutputSchema(ai.BackendGemini, req)
	require.NoError(t, err)
	assert.False(t, handled, "Gemini should retain Genkit's WithOutputType path")
}
