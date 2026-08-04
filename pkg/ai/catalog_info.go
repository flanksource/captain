package ai

import (
	"os/exec"
	"slices"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

// ModelInfo is the JSON shape served at GET /api/chat/models so a client model
// selector can be data-driven. Configured reports whether the model is
// selectable (its API provider has a key, or its agent backend is installed).
type ModelInfo struct {
	ID           string           `json:"id"`
	Provider     string           `json:"provider"`
	Label        string           `json:"label"`
	Runtime      api.Model        `json:"runtime"`
	Reasoning    bool             `json:"reasoning"`
	Temperature  bool             `json:"temperature"`
	Configured   bool             `json:"configured"`
	Availability api.Availability `json:"availability"`
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
	return catalogInfoFrom(Catalog(), catalogInfoOptions{ConfiguredProviders: configuredProviders})
}

type catalogInfoOptions struct {
	ConfiguredProviders []string
	Adapters            []AdapterStatus
}

// catalogInfoFrom annotates an arbitrary model list with selectability, shared
// by CatalogInfo (static catalog) and LiveCatalogInfo (whoami-probed catalog).
//
// Static execution menus arrive filtered; live descriptive menus may retain
// disabled rows so this seam also supplies their reason and remediation.
func catalogInfoFrom(models []Model, options catalogInfoOptions) []ModelInfo {
	out := make([]ModelInfo, 0, len(models))
	for _, m := range models {
		availability := modelAvailability(m, options)
		runtime := api.Model{
			Name:    m.BareID(),
			Backend: m.Backend,
		}.Capabilities()
		if m.ID != runtime.Name {
			runtime.ID = m.ID
		}
		out = append(out, ModelInfo{
			ID:              m.ID,
			Provider:        BackendToProvider(m.Backend),
			Label:           m.Label,
			Runtime:         runtime,
			Reasoning:       m.Reasoning,
			Temperature:     m.Temperature,
			Configured:      availability.IsAvailable(),
			Availability:    availability,
			Default:         m.ID == registry.DefaultModelID && availability.State != api.AvailabilityDisabled,
			ContextWindow:   m.ContextWindow,
			InputMediaTypes: append([]string(nil), m.InputMediaTypes...),
		})
	}
	return out
}

func modelAvailability(model Model, options catalogInfoOptions) api.Availability {
	disabled := Disabled()
	if disabled.Model(model.Backend, model.BareID()) {
		reason := disabled.Reason(model.Backend)
		if reason == "" {
			reason = "model " + model.ID
		}
		return api.Availability{State: api.AvailabilityDisabled,
			Reason:      "Disabled by " + reason + " in Captain configuration.",
			Remediation: "Enable " + reason + " on the Whoami page, then refresh."}
	}
	if slices.Contains(options.ConfiguredProviders, BackendToProvider(model.Backend)) {
		return api.Available()
	}
	for _, adapter := range options.Adapters {
		if adapter.Backend == string(model.Backend) {
			return AvailabilityForAdapter(adapter)
		}
	}
	if model.IsAgent() && agentBackendAvailable(model.Backend) {
		return api.Available()
	}
	if model.IsAgent() {
		return AvailabilityForAdapter(AdapterStatus{Backend: string(model.Backend), Type: "cli", BinaryMissing: requiredBinary(model.Backend)})
	}
	return AvailabilityForAdapter(AdapterStatus{Backend: string(model.Backend), Type: "api"})
}

func requiredBinary(backend Backend) string {
	switch backend {
	case BackendCodexCLI, BackendCodexAgent, BackendCodexCmux:
		return "codex"
	case BackendClaudeCLI, BackendClaudeCmux:
		return "claude"
	case BackendClaudeAgent:
		return "tsx"
	case BackendGeminiCLI:
		return "gemini"
	default:
		return string(backend)
	}
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
