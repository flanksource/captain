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
	Mode       string `json:"mode"`
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
// back-filled from the legacy flat keys, with the mode defaulting to api and the
// model to that runtime's built-in default.
func EffectiveDefaults(saved captainconfig.AIDefaults, provider *registry.Provider) (ProviderDefaultView, error) {
	if provider == nil {
		return ProviderDefaultView{}, fmt.Errorf("provider is required")
	}
	configured, exists := saved.Providers[provider.Name]
	mode := registry.RuntimeMode(strings.TrimSpace(configured.Mode))
	if mode == "" {
		mode = provider.DefaultMode
	}
	if _, err := provider.RequireMode(mode); err != nil {
		return ProviderDefaultView{}, err
	}
	model := strings.TrimSpace(configured.Model)
	if model == "" {
		model = DefaultModelFor(provider, mode)
	}
	effort := registry.Effort(strings.TrimSpace(configured.ReasoningEffort))
	if err := effort.Validate(); err != nil {
		return ProviderDefaultView{}, err
	}
	return ProviderDefaultView{
		Mode: string(mode), Model: model, Effort: string(effort), Configured: exists,
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
	provider := model.Provider
	if provider == nil && strings.TrimSpace(model.Name) != "" {
		p, err := registry.ProviderFor(model.Name)
		if err != nil {
			return registry.Model{}, err
		}
		provider = p
	}
	if provider == nil && allowActive {
		provider, _ = registry.ProviderByName(saved.ActiveProvider())
	}
	if provider == nil {
		return registry.Model{}, fmt.Errorf("provider cannot be resolved for model %q", model.Name)
	}
	defaults, err := EffectiveDefaults(saved, provider)
	if err != nil {
		return registry.Model{}, err
	}
	// An explicit --mode owns the mechanism; the saved default only fills a gap.
	if model.Mode == "" {
		model.Mode = registry.RuntimeMode(defaults.Mode)
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
		defaults, err := EffectiveDefaults(saved, p)
		if err != nil {
			return nil, err
		}
		out[p.Name] = defaults
	}
	return out, nil
}

// DefaultModelFor returns a hard-coded picker default per runtime that seeds the
// form. Local transports use exact provider model IDs from the catalog so the
// seeded default is a selectable option. The API mode has no "default" flag on
// /v1/models, so we use the most-current id we expect each provider to keep
// stable; the user can pick anything else.
//
// This stays a hand-maintained table rather than "the catalog's newest preferred
// model": these are the values `captain configure` seeds and users have saved, and
// deriving them would silently move every unconfigured user's default whenever the
// catalog snapshot updates.
func DefaultModelFor(p *registry.Provider, mode registry.RuntimeMode) string {
	if p == nil {
		return ""
	}
	switch p.Name {
	case registry.Anthropic.Name:
		return "claude-sonnet-5"
	case registry.OpenAI.Name:
		// The only provider whose local transports seed a different model than its
		// API: codex names its own coding-tuned id.
		if mode.Kind() == "cli" {
			return "gpt-5.6-sol"
		}
		return "gpt-5.6"
	case registry.Google.Name:
		return "gemini-3.5-flash"
	case registry.DeepSeek.Name:
		// The reasoning-tuned member of the current line. "deepseek-reasoner" was
		// the previous generation's id and is no longer in the catalog, so it
		// seeded a picker with a model the provider cannot run.
		return "deepseek-v4-pro"
	}
	return ""
}
