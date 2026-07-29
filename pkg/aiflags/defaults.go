package aiflags

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// ProviderDefaultView is one provider's effective saved defaults, with the legacy
// flat config projected in and validated.
type ProviderDefaultView struct {
	Agent      string `json:"agent"`
	Model      string `json:"model"`
	Effort     string `json:"effort"`
	Configured bool   `json:"configured"`
}

// LoadDefaults reads the saved AI defaults from ~/.captain.yaml.
//
// It reports a broken config rather than swallowing it: whether to degrade to zero
// defaults and carry on is a CLI policy, not a library's call. captain's own
// commands make that choice in pkg/cli (loadSavedAI warns and continues); callers
// wanting the same behaviour pass their own AIDefaults to ResolveWith.
//
// Deliberately no logger here — commons/logger pulls ~55 packages (prometheus,
// fsnotify, …) and would cost this leaf its entire reason for existing.
func LoadDefaults() (captainconfig.AIDefaults, error) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return captainconfig.AIDefaults{}, err
	}
	return cfg.AI, nil
}

// EffectiveDefaults resolves one provider's saved defaults: the per-provider block
// back-filled from the legacy flat keys, with the agent defaulting to the provider
// itself and the model to that agent's built-in default.
func EffectiveDefaults(saved captainconfig.AIDefaults, provider registry.Backend) (ProviderDefaultView, error) {
	if provider.Provider() != provider {
		return ProviderDefaultView{}, fmt.Errorf("invalid provider %q", provider)
	}
	configured, exists := saved.Providers[string(provider)]
	legacy := saved.Provider(string(provider))
	if configured.Agent == "" {
		configured.Agent = legacy.Agent
	}
	if configured.Model == "" {
		configured.Model = legacy.Model
	}
	if configured.ReasoningEffort == "" {
		configured.ReasoningEffort = legacy.ReasoningEffort
	}
	agent := registry.Backend(strings.TrimSpace(configured.Agent))
	if agent == "" {
		agent = provider
	}
	if agent.Provider() != provider {
		return ProviderDefaultView{}, fmt.Errorf("agent %q does not belong to provider %q", agent, provider)
	}
	model := strings.TrimSpace(configured.Model)
	if model == "" {
		model = DefaultModelFor(agent)
	}
	effort := registry.Effort(strings.TrimSpace(configured.ReasoningEffort))
	if err := effort.Validate(); err != nil {
		return ProviderDefaultView{}, err
	}
	return ProviderDefaultView{
		Agent: string(agent), Model: model, Effort: string(effort), Configured: exists,
	}, nil
}

// ApplyDefaults fills a model's unset fields from the saved per-provider defaults,
// primary and fallbacks alike. It expects an already-expanded model (see the
// package doc) and does not resolve — the caller resolves once, afterwards.
func ApplyDefaults(model registry.Model, saved captainconfig.AIDefaults) (registry.Model, error) {
	var err error
	if strings.TrimSpace(model.Name) != "" {
		if model, err = model.Expand(); err != nil {
			return registry.Model{}, err
		}
	}
	if model, err = applyCandidateDefaults(model, saved, true); err != nil {
		return registry.Model{}, err
	}
	for i := range model.Fallbacks {
		fallback := model.Fallbacks[i]
		if fallback, err = fallback.Expand(); err != nil {
			return registry.Model{}, fmt.Errorf("fallback[%d]: %w", i, err)
		}
		// allowActive=false: a fallback must not silently become the active
		// provider's model — that would make the fallback chain a no-op.
		if fallback, err = applyCandidateDefaults(fallback, saved, false); err != nil {
			return registry.Model{}, fmt.Errorf("fallback[%d]: %w", i, err)
		}
		model.Fallbacks[i] = fallback
	}
	return model, nil
}

func applyCandidateDefaults(model registry.Model, saved captainconfig.AIDefaults, allowActive bool) (registry.Model, error) {
	provider := model.Backend.Provider()
	if provider == "" && strings.TrimSpace(model.Name) != "" {
		backend, err := registry.InferBackend(model.Name)
		if err != nil {
			return registry.Model{}, err
		}
		provider = backend.Provider()
	}
	if provider == "" && allowActive {
		provider = registry.Backend(saved.ActiveProvider())
	}
	if provider == "" {
		return registry.Model{}, fmt.Errorf("provider cannot be resolved for model %q", model.Name)
	}
	defaults, err := EffectiveDefaults(saved, provider)
	if err != nil {
		return registry.Model{}, err
	}
	// An explicit --mode owns the mechanism. Taking the saved agent here would make
	// `--mode cli` against a saved claude-agent default fail as "mode cli
	// contradicts backend claude-agent" — a contradiction the user never wrote.
	if model.Backend == "" && model.Mode == "" {
		model.Backend = registry.Backend(defaults.Agent)
	}
	if strings.TrimSpace(model.Name) == "" {
		model.Name = defaults.Model
	}
	if model.Effort == registry.EffortNone {
		model.Effort = registry.Effort(defaults.Effort)
	}
	// Preserve valid requested tiers here; execution resolves them against the
	// exact provider model after configuration and selector overlays are complete.
	if err := model.Effort.Validate(); err != nil {
		return registry.Model{}, err
	}
	return model, nil
}

// AllProviderDefaults resolves every provider's effective defaults.
func AllProviderDefaults(saved captainconfig.AIDefaults) (map[string]ProviderDefaultView, error) {
	out := make(map[string]ProviderDefaultView, len(registry.Providers()))
	for _, p := range registry.Providers() {
		provider, err := p.BackendFor(registry.ModeAPI)
		if err != nil {
			return nil, err
		}
		defaults, err := EffectiveDefaults(saved, provider)
		if err != nil {
			return nil, err
		}
		out[string(provider)] = defaults
	}
	return out, nil
}

// DefaultModelFor returns a hard-coded picker default per backend that seeds the
// form. CLI/agent backends use exact provider model IDs from the catalog so the
// seeded default is a selectable option. API backends have no "default" flag on
// /v1/models, so we use the most-current id we expect each provider to keep
// stable; the user can pick anything else.
//
// This stays a hand-maintained table rather than "the catalog's newest preferred
// model": these are the values `captain configure` seeds and users have saved, and
// deriving them would silently move every unconfigured user's default whenever the
// catalog snapshot updates.
func DefaultModelFor(b registry.Backend) string {
	switch b {
	case registry.BackendAnthropic:
		return "claude-sonnet-5"
	case registry.BackendClaudeCLI, registry.BackendClaudeAgent, registry.BackendClaudeCmux:
		return "claude-sonnet-5"
	case registry.BackendOpenAI:
		return "gpt-5.6"
	case registry.BackendDeepSeek:
		return "deepseek-reasoner"
	case registry.BackendCodexCLI, registry.BackendCodexAgent, registry.BackendCodexCmux:
		return "gpt-5.6-sol"
	case registry.BackendGemini, registry.BackendGeminiCLI:
		return "gemini-3.5-flash"
	}
	return ""
}
