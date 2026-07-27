package cli

import "github.com/flanksource/captain/pkg/ai"

// agentCatalogModels returns the model list for a CLI/agent backend from
// captain's static catalog — the key-free source of truth shared with the chat
// menu and shell completion. CLI/agent backends authenticate internally
// (subscription/OAuth via the installed binary), so enumerating their models
// must never require the parent provider's API key.
//
// The returned ID is the exact provider model ID Captain sends at runtime.
// CLI/cmux modes share the corresponding agent catalog entries.
func agentCatalogModels(b ai.Backend) []ai.ModelDef {
	return ai.AgentCatalogModels(b)
}
