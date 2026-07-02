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
		{ID: "claude-agent-opus", Backend: ai.BackendClaudeAgent, Label: "Claude Agent · Opus"},
		{ID: "claude-agent-sonnet", Backend: ai.BackendClaudeAgent, Label: "Claude Agent · Sonnet"},
		{ID: "codex-gpt-5-codex", Backend: ai.BackendCodexCLI, AgentModel: "gpt-5-codex", Label: "Codex · GPT-5"},
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
			// AgentModel slug wins over the catalog ID: codex receives the
			// model string verbatim, so it must be "gpt-5-codex" not
			// "codex-gpt-5-codex".
			backend: ai.BackendCodexCLI,
			want:    []ai.ModelDef{{ID: "gpt-5-codex", Name: "Codex · GPT-5", Backend: ai.BackendCodexCLI}},
		},
		{
			// Sorted by ID; genkit (API) anthropic entry excluded.
			backend: ai.BackendClaudeAgent,
			want: []ai.ModelDef{
				{ID: "claude-agent-opus", Name: "Claude Agent · Opus", Backend: ai.BackendClaudeAgent},
				{ID: "claude-agent-sonnet", Name: "Claude Agent · Sonnet", Backend: ai.BackendClaudeAgent},
			},
		},
		{
			// claude-cli reuses the claude-agent entries but is re-tagged with
			// its own backend.
			backend: ai.BackendClaudeCLI,
			want: []ai.ModelDef{
				{ID: "claude-agent-opus", Name: "Claude Agent · Opus", Backend: ai.BackendClaudeCLI},
				{ID: "claude-agent-sonnet", Name: "Claude Agent · Sonnet", Backend: ai.BackendClaudeCLI},
			},
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
