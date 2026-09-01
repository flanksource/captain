package genkit

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observation"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credentials"

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
	}, mapUsage(raw, ai.Anthropic))

	// Gemini: PromptTokenCount folds in cache → net input; CandidatesTokenCount
	// excludes thoughts → output passes through.
	assert.Equal(t, ai.Usage{
		InputTokens:     103, // 120 - 17
		OutputTokens:    45,
		ReasoningTokens: 30,
		CacheReadTokens: 17,
	}, mapUsage(raw, ai.Google))

	// OpenAI/DeepSeek (compat_oai): prompt_tokens folds in cache AND
	// completion_tokens folds in reasoning → net both.
	openaiWant := ai.Usage{
		InputTokens:     103, // 120 - 17
		OutputTokens:    15,  // 45 - 30
		ReasoningTokens: 30,
		CacheReadTokens: 17,
	}
	assert.Equal(t, openaiWant, mapUsage(raw, ai.OpenAI))
	assert.Equal(t, openaiWant, mapUsage(raw, ai.DeepSeek))

	// Disjoint invariant: for cache-folding providers InputTokens no longer
	// overlaps CacheReadTokens, so pricing cannot bill the cached prefix twice.
	for _, provider := range []*ai.ModelProvider{ai.Google, ai.OpenAI, ai.DeepSeek} {
		got := mapUsage(raw, provider)
		assert.Equal(t, raw.InputTokens, got.InputTokens+got.CacheReadTokens, "%s input+cache", provider.Name)
	}

	assert.Equal(t, ai.Usage{}, mapUsage(nil, ai.Anthropic))
}

func TestResponseToResponseRecordsNativeUsagePresence(t *testing.T) {
	tests := []struct {
		name      string
		usage     *gkai.GenerationUsage
		wantUsage bool
	}{
		{name: "omitted", usage: nil, wantUsage: false},
		{name: "present zero", usage: &gkai.GenerationUsage{}, wantUsage: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := observation.NewRecorder()
			ctx := observation.ContextWithRecorder(context.Background(), recorder)
			responseToResponse(ctx, &gkai.ModelResponse{Usage: test.usage}, ai.OpenAI, "gpt-5", time.Now())

			usage := recorder.Snapshot().Usage
			if (usage != nil) != test.wantUsage {
				t.Fatalf("recorded usage = %#v, want present %t", usage, test.wantUsage)
			}
			if usage != nil && *usage != (ai.Usage{}) {
				t.Fatalf("recorded known-zero usage = %#v", usage)
			}
		})
	}
}

func TestModelRef(t *testing.T) {
	tests := []struct {
		name     string
		provider *ai.ModelProvider
		model    string
		want     string
		wantErr  bool
	}{
		{"anthropic bare", ai.Anthropic, "claude-sonnet-4", "anthropic/claude-sonnet-4", false},
		{"openai bare", ai.OpenAI, "gpt-4o", "openai/gpt-4o", false},
		{"gemini bare", ai.Google, "gemini-2.5-pro", "googleai/gemini-2.5-pro", false},
		{"gemini normalizes models prefix", ai.Google, "models/gemini-2.5-pro", "googleai/gemini-2.5-pro", false},
		{"deepseek bare", ai.DeepSeek, "deepseek-chat", "deepseek/deepseek-chat", false},
		{"deepseek re-prefixes existing", ai.DeepSeek, "deepseek/deepseek-reasoner", "deepseek/deepseek-reasoner", false},
		{"anthropic re-prefixes existing", ai.Anthropic, "anthropic/claude-opus-4", "anthropic/claude-opus-4", false},
		{"unsupported provider errors", nil, "claude-code-foo", "", true},
		{"empty model errors", ai.Anthropic, "", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := modelRef(tt.provider, tt.model)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// genkit sends the id it is given and only namespaces it for the genkit ref.
// It used to re-resolve the name itself, which meant the id captain recorded
// and the id the API received could differ.
func TestNewForwardsTheResolvedModelID(t *testing.T) {
	resolved, err := api.ResolveModel(api.Model{Name: "opus-4-8", Mode: api.ModeAPI})
	require.NoError(t, err)
	require.Equal(t, "claude-opus-4-8", resolved.Name, "Resolve owns the rewrite this adapter must not redo")

	p, err := New(ai.Config{Model: resolved, APIKey: "test-anthropic-key"})
	require.NoError(t, err)

	assert.Equal(t, "claude-opus-4-8", p.GetModel())
	assert.Equal(t, "anthropic/claude-opus-4-8", p.modelRef)
}

func TestPricingModelID(t *testing.T) {
	assert.Equal(t, "anthropic/claude-sonnet-4", pricingModelID(ai.Anthropic, "claude-sonnet-4"))
	assert.Equal(t, "openai/gpt-4o", pricingModelID(ai.OpenAI, "gpt-4o"))
	// Gemini's OpenRouter pricing key is google/, not the genkit googleai/ ref.
	assert.Equal(t, "google/gemini-2.5-pro", pricingModelID(ai.Google, "googleai/gemini-2.5-pro"))
	assert.Equal(t, "deepseek/deepseek-chat", pricingModelID(ai.DeepSeek, "deepseek/deepseek-chat"))
}

func TestNewMissingAPIKey(t *testing.T) {
	credentials.SetPathForTesting(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { credentials.SetPathForTesting("") })
	// Ensure no provider key leaks in from the environment.
	for _, env := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "DEEPSEEK_API_KEY"} {
		t.Setenv(env, "")
	}

	for _, provider := range []*ai.ModelProvider{ai.Anthropic, ai.OpenAI, ai.Google, ai.DeepSeek} {
		t.Run(provider.Name, func(t *testing.T) {
			_, err := New(ai.Config{Model: api.Model{Provider: provider, Mode: ai.ModeAPI, Name: "some-model"}})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "no API key")
			assert.ErrorIs(t, err, ai.ErrNoAPIKey)
		})
	}
}

// genkit is the api mode. A model recording another one is a routing bug: this
// adapter would run it and then report a runtime it never used.
func TestNewRejectsANonAPIMode(t *testing.T) {
	_, err := New(ai.Config{Model: api.Model{Provider: ai.Anthropic, Mode: ai.ModeCLI, Name: "claude-opus-5"}, APIKey: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not support cli mode")

	// An unresolved model names no provider, so it cannot be routed at all.
	_, err = New(ai.Config{Model: api.Model{Name: "claude-opus-5"}, APIKey: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs a resolved model")
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

func TestRuntimeOutputSchema_NormalizesOpenAIStructuredOutput(t *testing.T) {
	type answer struct {
		Answer string `json:"answer"`
		Detail string `json:"detail,omitempty"`
	}
	req := ai.Request{Prompt: api.Prompt{Schema: &answer{}}}

	schema, handled, err := runtimeOutputSchema(ai.OpenAI, req)
	require.NoError(t, err)
	require.True(t, handled)
	assert.Equal(t, []any{"answer", "detail"}, schema["required"])
	assert.Equal(t, false, schema["additionalProperties"])

	_, handled, err = runtimeOutputSchema(ai.Google, req)
	require.NoError(t, err)
	assert.False(t, handled, "Gemini should retain Genkit's WithOutputType path")
}
