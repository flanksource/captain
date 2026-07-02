package cli

import "github.com/flanksource/captain/pkg/ai"

// agentCatalogModels returns the model list for a CLI/agent backend from
// captain's static catalog — the key-free source of truth shared with the chat
// menu and shell completion. CLI/agent backends authenticate internally
// (subscription/OAuth via the installed binary), so enumerating their models
// must never require the parent provider's API key.
//
// The returned ID is the slug the captain provider expects at run time:
// AgentModel when the catalog sets one (e.g. codex's "gpt-5-codex", which the
// app-server sends verbatim) otherwise the catalog ID (e.g. "claude-agent-sonnet",
// which the claude-agent provider de-prefixes itself). claude-cli shares the
// claude-agent provider, so it shares its catalog entries.
func agentCatalogModels(b ai.Backend) []ai.ModelDef {
	return ai.AgentCatalogModels(b)
}
