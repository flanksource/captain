package cli

import (
	"sort"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/clicky/aichat"
)

// agentCatalogModels returns the model list for a CLI/agent backend from
// clicky/aichat's static catalog — the key-free source of truth shared with the
// chat menu and shell completion. CLI/agent backends authenticate internally
// (subscription/OAuth via the installed binary), so enumerating their models
// must never require the parent provider's API key.
//
// The returned ID is the slug the captain provider expects at run time:
// AgentModel when the catalog sets one (e.g. codex's "gpt-5-codex", which the
// app-server sends verbatim) otherwise the catalog ID (e.g. "claude-agent-sonnet",
// which the claude-agent provider de-prefixes itself). claude-cli shares the
// claude-agent provider, so it shares its catalog entries.
func agentCatalogModels(b ai.Backend) []ai.ModelDef {
	want := b
	if want == ai.BackendClaudeCLI {
		want = ai.BackendClaudeAgent
	}

	out := []ai.ModelDef{}
	for _, m := range aichat.Catalog() {
		if m.Engine != aichat.EngineAgent || m.Backend != want {
			continue
		}
		id := m.ID
		if m.AgentModel != "" {
			id = m.AgentModel
		}
		label := m.Label
		if label == "" {
			label = id
		}
		out = append(out, ai.ModelDef{ID: id, Name: label, Backend: b})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
