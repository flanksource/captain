package cli

import (
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
)

// installTestCatalog swaps in a deterministic catalog for the duration of the
// test so the assertions don't drift with the shipped model list. The catalog
// mixes an api entry with agent entries for two families so per-runtime
// filtering is exercised.
func installTestCatalog(t *testing.T) {
	t.Helper()
	t.Cleanup(ai.ResetModelCatalog)

	if err := ai.SetModelCatalog([]ai.Model{
		{ID: "anthropic/claude-sonnet-4-5", Provider: ai.Anthropic, Mode: ai.ModeAPI, Label: "Claude Sonnet 4.5"},
		{ID: "claude-opus-4-8", Provider: ai.Anthropic, Mode: ai.ModeAgent, Label: "Claude Agent · Opus 4.8"},
		{ID: "claude-sonnet-5", Provider: ai.Anthropic, Mode: ai.ModeAgent, Label: "Claude Agent · Sonnet 5"},
		{ID: "gpt-5.5", Provider: ai.OpenAI, Mode: ai.ModeAgent, Label: "Codex Agent · GPT-5.5"},
	}); err != nil {
		t.Fatalf("SetModelCatalog: %v", err)
	}
}

func TestAgentCatalogModels(t *testing.T) {
	installTestCatalog(t)

	cases := []struct {
		provider *ai.ModelProvider
		mode     ai.RuntimeMode
		want     []ai.ModelDef
	}{
		{
			provider: ai.OpenAI,
			mode:     ai.ModeCLI,
			want:     []ai.ModelDef{{ID: "gpt-5.5", Name: "Codex Agent · GPT-5.5", Provider: "openai", Mode: ai.ModeCLI, CapabilitiesKnown: true}},
		},
		{
			provider: ai.OpenAI,
			mode:     ai.ModeAgent,
			want:     []ai.ModelDef{{ID: "gpt-5.5", Name: "Codex Agent · GPT-5.5", Provider: "openai", Mode: ai.ModeAgent, CapabilitiesKnown: true}},
		},
		{
			// Sorted by ID; genkit (API) anthropic entry excluded.
			provider: ai.Anthropic,
			mode:     ai.ModeAgent,
			want: []ai.ModelDef{
				{ID: "claude-opus-4-8", Name: "Claude Agent · Opus 4.8", Provider: "anthropic", Mode: ai.ModeAgent, CapabilitiesKnown: true},
				{ID: "claude-sonnet-5", Name: "Claude Agent · Sonnet 5", Provider: "anthropic", Mode: ai.ModeAgent, CapabilitiesKnown: true},
			},
		},
		{
			// claude-cli reuses the claude-agent entries but is re-tagged with
			// its own backend.
			provider: ai.Anthropic,
			mode:     ai.ModeCLI,
			want: []ai.ModelDef{
				{ID: "claude-opus-4-8", Name: "Claude Agent · Opus 4.8", Provider: "anthropic", Mode: ai.ModeCLI, CapabilitiesKnown: true},
				{ID: "claude-sonnet-5", Name: "Claude Agent · Sonnet 5", Provider: "anthropic", Mode: ai.ModeCLI, CapabilitiesKnown: true},
			},
		},
		{
			// claude-cmux uses the same local Claude catalog and is re-tagged with
			// its own backend.
			provider: ai.Anthropic,
			mode:     ai.ModeCmux,
			want: []ai.ModelDef{
				{ID: "claude-opus-4-8", Name: "Claude Agent · Opus 4.8", Provider: "anthropic", Mode: ai.ModeCmux, CapabilitiesKnown: true},
				{ID: "claude-sonnet-5", Name: "Claude Agent · Sonnet 5", Provider: "anthropic", Mode: ai.ModeCmux, CapabilitiesKnown: true},
			},
		},
		{
			// codex-cmux shares the codex-agent runtime model slug.
			provider: ai.OpenAI,
			mode:     ai.ModeCmux,
			want:     []ai.ModelDef{{ID: "gpt-5.5", Name: "Codex Agent · GPT-5.5", Provider: "openai", Mode: ai.ModeCmux, CapabilitiesKnown: true}},
		},
		{
			// No catalog entries for gemini-cli: empty, never an error.
			provider: ai.Google,
			mode:     ai.ModeCLI,
			want:     []ai.ModelDef{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.provider.Name+"/"+string(tc.mode), func(t *testing.T) {
			got := agentCatalogModels(tc.provider, tc.mode)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("agentCatalogModels(%s, %s) = %+v, want %+v", tc.provider.Name, tc.mode, got, tc.want)
			}
		})
	}
}
