package cli

import (
	"reflect"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/clicky/aichat"
)

// installTestCatalog swaps in a deterministic catalog for the duration of the
// test so the assertions don't drift with clicky's shipped model list. The
// catalog mixes a genkit (API) entry with agent entries for two backends so the
// filtering — agent-engine only, exact backend match — is actually exercised.
func installTestCatalog(t *testing.T) {
	t.Helper()
	prev := aichat.Catalog()
	t.Cleanup(func() { _ = aichat.SetModelCatalog(prev) })

	if err := aichat.SetModelCatalog([]aichat.Model{
		{ID: "anthropic/claude-sonnet-4-5", Provider: aichat.ProviderAnthropic, Label: "Claude Sonnet 4.5"},
		{ID: "claude-agent-opus", Engine: aichat.EngineAgent, Backend: ai.BackendClaudeAgent, Provider: "claude-agent", Label: "Claude Agent · Opus"},
		{ID: "claude-agent-sonnet", Engine: aichat.EngineAgent, Backend: ai.BackendClaudeAgent, Provider: "claude-agent", Label: "Claude Agent · Sonnet"},
		{ID: "codex-gpt-5-codex", Engine: aichat.EngineAgent, Backend: ai.BackendCodexCLI, AgentModel: "gpt-5-codex", Provider: "codex-cli", Label: "Codex · GPT-5"},
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
