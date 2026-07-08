package cli

import (
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
)

// installTestCatalog swaps in a deterministic catalog for the duration of the
// test so the assertions don't drift with the shipped model list. The catalog
// mixes an API entry with agent entries for two backends so exact backend
// filtering is exercised.
func installTestCatalog(t *testing.T) {
	t.Helper()
	t.Cleanup(ai.ResetModelCatalog)

	if err := ai.SetModelCatalog([]ai.Model{
		{ID: "anthropic/claude-sonnet-4-5", Backend: ai.BackendAnthropic, Label: "Claude Sonnet 4.5"},
		{ID: "claude-opus-4-8", Backend: ai.BackendClaudeAgent, Label: "Claude Agent · Opus 4.8"},
		{ID: "claude-sonnet-5", Backend: ai.BackendClaudeAgent, Label: "Claude Agent · Sonnet 5"},
		{ID: "gpt-5.5", Backend: ai.BackendCodexAgent, Label: "Codex Agent · GPT-5.5"},
	}); err != nil {
		t.Fatalf("SetModelCatalog: %v", err)
	}
}

func TestAgentCatalogModels(t *testing.T) {
	installTestCatalog(t)

	cases := []struct {
		backend ai.Backend
		want    []ai.ModelDef
	}{
		{
			backend: ai.BackendCodexCLI,
			want:    []ai.ModelDef{{ID: "gpt-5.5", Name: "Codex Agent · GPT-5.5", Backend: ai.BackendCodexCLI}},
		},
		{
			backend: ai.BackendCodexAgent,
			want:    []ai.ModelDef{{ID: "gpt-5.5", Name: "Codex Agent · GPT-5.5", Backend: ai.BackendCodexAgent}},
		},
		{
			// Sorted by ID; genkit (API) anthropic entry excluded.
			backend: ai.BackendClaudeAgent,
			want: []ai.ModelDef{
				{ID: "claude-opus-4-8", Name: "Claude Agent · Opus 4.8", Backend: ai.BackendClaudeAgent},
				{ID: "claude-sonnet-5", Name: "Claude Agent · Sonnet 5", Backend: ai.BackendClaudeAgent},
			},
		},
		{
			// claude-cli reuses the claude-agent entries but is re-tagged with
			// its own backend.
			backend: ai.BackendClaudeCLI,
			want: []ai.ModelDef{
				{ID: "claude-opus-4-8", Name: "Claude Agent · Opus 4.8", Backend: ai.BackendClaudeCLI},
				{ID: "claude-sonnet-5", Name: "Claude Agent · Sonnet 5", Backend: ai.BackendClaudeCLI},
			},
		},
		{
			// claude-cmux uses the same local Claude catalog and is re-tagged with
			// its own backend.
			backend: ai.BackendClaudeCmux,
			want: []ai.ModelDef{
				{ID: "claude-opus-4-8", Name: "Claude Agent · Opus 4.8", Backend: ai.BackendClaudeCmux},
				{ID: "claude-sonnet-5", Name: "Claude Agent · Sonnet 5", Backend: ai.BackendClaudeCmux},
			},
		},
		{
			// codex-cmux shares the codex-agent runtime model slug.
			backend: ai.BackendCodexCmux,
			want:    []ai.ModelDef{{ID: "gpt-5.5", Name: "Codex Agent · GPT-5.5", Backend: ai.BackendCodexCmux}},
		},
		{
			// No catalog entries for gemini-cli: empty, never an error.
			backend: ai.BackendGeminiCLI,
			want:    []ai.ModelDef{},
		},
	}

	for _, tc := range cases {
		t.Run(string(tc.backend), func(t *testing.T) {
			got := agentCatalogModels(tc.backend)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("agentCatalogModels(%s) = %+v, want %+v", tc.backend, got, tc.want)
			}
		})
	}
}
