package ai

import (
	"os/exec"
	"slices"

	"github.com/flanksource/captain/pkg/api/registry"
)

// ModelInfo is the JSON shape served at GET /api/chat/models so a client model
// selector can be data-driven. Configured reports whether the model is
// selectable (its API provider has a key, or its agent backend is installed).
type ModelInfo struct {
	ID          string `json:"id"`
	Provider    string `json:"provider"`
	Label       string `json:"label"`
	Reasoning   bool   `json:"reasoning"`
	Temperature bool   `json:"temperature"`
	Configured  bool   `json:"configured"`
	// Default marks captain's declared default model, so a client seeds its
	// picker from the menu instead of hardcoding an id that rots on the next
	// release. At most one row carries it, and none does when that model is
	// disabled — the client then falls back to the first configured row.
	Default         bool     `json:"default,omitempty"`
	ContextWindow   int      `json:"contextWindow"`
	InputMediaTypes []string `json:"inputMediaTypes"`
}

// BackendToProvider maps a backend to the provider string used in API model ids
// and the web menu's "provider" field. API backends use the genkit provider
// namespace (anthropic/openai/googleai); agent/CLI backends use their backend
// string verbatim.
// BackendToProvider is the provider key the webapp and the session store use.
//
// Its output is a frozen wire format: it is persisted to sessions.provider and
// prompt_runs.runtime.resolved.provider, and /api/chat/models matches on it. The
// API backends map to their catalog namespace ("googleai" for Gemini, not
// "google"); every other backend is its own key verbatim.
func BackendToProvider(b Backend) string {
	p, mode, ok := registry.ProviderFor(b)
	if !ok || mode != registry.ModeAPI {
		return string(b)
	}
	return p.CatalogPrefix
}

// CatalogInfo returns the static model menu annotated with selectability. API
// models are configured when their provider is among configuredProviders (the
// keys the caller resolved); agent/CLI models are configured when their local
// backend binary is installed. Order mirrors the catalog so the client renders a
// stable, grouped menu.
func CatalogInfo(configuredProviders []string) []ModelInfo {
	return catalogInfoFrom(Catalog(), configuredProviders)
}

// catalogInfoFrom annotates an arbitrary model list with selectability, shared
// by CatalogInfo (static catalog) and LiveCatalogInfo (whoami-probed catalog).
//
// Both inputs arrive already filtered — Catalog() drops disabled models at read
// time and mergeLiveCatalog drops disabled probe rows — so this only annotates.
func catalogInfoFrom(models []Model, configuredProviders []string) []ModelInfo {
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		configured := false
		if m.IsAgent() {
			configured = agentBackendAvailable(m.Backend)
		} else {
			configured = slices.Contains(configuredProviders, BackendToProvider(m.Backend))
		}
		out = append(out, ModelInfo{
			ID:              m.ID,
			Provider:        BackendToProvider(m.Backend),
			Label:           m.Label,
			Reasoning:       m.Reasoning,
			Temperature:     m.Temperature,
			Configured:      configured,
			Default:         m.ID == registry.DefaultModelID,
			ContextWindow:   m.ContextWindow,
			InputMediaTypes: append([]string(nil), m.InputMediaTypes...),
		})
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
