package ai

import (
	"os/exec"
	"slices"
)

// ModelInfo is the JSON shape served at GET /api/chat/models so a client model
// selector can be data-driven. Configured reports whether the model is
// selectable (its API provider has a key, or its agent backend is installed).
type ModelInfo struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Label         string `json:"label"`
	Reasoning     bool   `json:"reasoning"`
	Temperature   bool   `json:"temperature"`
	Configured    bool   `json:"configured"`
	ContextWindow int    `json:"contextWindow"`
}

// BackendToProvider maps a backend to the provider string used in API model ids
// and the web menu's "provider" field. API backends use the genkit provider
// namespace (anthropic/openai/googleai); agent/CLI backends use their backend
// string verbatim.
func BackendToProvider(b Backend) string {
	switch b {
	case BackendAnthropic:
		return "anthropic"
	case BackendOpenAI:
		return "openai"
	case BackendGemini:
		return "googleai"
	case BackendDeepSeek:
		return "deepseek"
	default:
		return string(b)
	}
}

// CatalogInfo returns the model menu annotated with selectability. API models
// are configured when their provider is among configuredProviders (the keys the
// caller resolved); agent/CLI models are configured when their local backend
// binary is installed. Order mirrors the catalog so the client renders a stable,
// grouped menu.
func CatalogInfo(configuredProviders []string) []ModelInfo {
	models := Catalog()
	out := make([]ModelInfo, len(models))
	for i, m := range models {
		configured := false
		if m.IsAgent() {
			configured = agentBackendAvailable(m.Backend)
		} else {
			configured = slices.Contains(configuredProviders, BackendToProvider(m.Backend))
		}
		out[i] = ModelInfo{
			ID:            m.ID,
			Provider:      BackendToProvider(m.Backend),
			Label:         m.Label,
			Reasoning:     m.Reasoning,
			Temperature:   m.Temperature,
			Configured:    configured,
			ContextWindow: m.ContextWindow,
		}
	}
	return out
}

// agentBackendAvailable reports whether an agent backend's local binary is
// installed (best effort): codex backends need the `codex` binary; claude-cli
// needs `claude`; claude-agent needs `tsx`. A turn still fails loud if the probe
// is wrong.
func agentBackendAvailable(b Backend) bool {
	switch b {
	case BackendCodexCLI, BackendCodexAgent, BackendCodexCmux:
		return binaryOnPath("codex")
	case BackendClaudeCLI, BackendClaudeCmux:
		return binaryOnPath("claude")
	case BackendClaudeAgent:
		return binaryOnPath("tsx")
	default:
		return false
	}
}

func binaryOnPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}
