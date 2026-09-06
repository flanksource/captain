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
// Broken configuration is returned to the caller. Request pipelines capture one
// settings snapshot and pass its AIDefaults to the shared specification resolver.
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

// EffectiveDefaults SEEDS a form: one provider's saved defaults with the gaps
// filled from the registry — the mode from Provider.DefaultMode and the model
// from the built-in DefaultModelFor table.
//
// It is for `captain configure` and the whoami/picker surfaces, where proposing
// a value the user can accept or change is exactly right. It must NOT be used to
// decide what a run executes on: proposing there is the silent defaulting this
// package now refuses. See SavedDefaults.
func EffectiveDefaults(saved captainconfig.AIDefaults, provider *registry.Provider) (ProviderDefaultView, error) {
	view, err := SavedDefaults(saved, provider)
	if err != nil {
		return ProviderDefaultView{}, err
	}
	if view.Mode == "" {
		view.Mode = string(provider.DefaultMode)
	}
	if _, err := provider.RequireMode(registry.RuntimeMode(view.Mode)); err != nil {
		return ProviderDefaultView{}, err
	}
	if view.Model == "" {
		view.Model = DefaultModelFor(provider, registry.RuntimeMode(view.Mode))
	}
	return view, nil
}

// SavedDefaults RESOLVES a run: strictly what ~/.captain.yaml records for this
// provider, plus the global ai.defaultModel selector as a last config-owned
// fallback. Unset fields come back empty so the caller fails loudly instead of
// inheriting a compiled-in model or mode.
//
// The global default is a compact selector, so it can supply a mode as well as a
// name — which is the whole reason it can stand in for a missing provider block.
func SavedDefaults(saved captainconfig.AIDefaults, provider *registry.Provider) (ProviderDefaultView, error) {
	if provider == nil {
		return ProviderDefaultView{}, fmt.Errorf("provider is required")
	}
	if err := saved.Validate(); err != nil {
		return ProviderDefaultView{}, err
	}
	global, err := globalDefaultModel(saved)
	if err != nil {
		return ProviderDefaultView{}, err
	}
	defaults, err := savedProviderModel(saved, provider, global)
	if err != nil {
		return ProviderDefaultView{}, err
	}
	model := defaults.Model
	if model.Mode == "" {
		if modes := provider.Modes(); len(modes) == 1 {
			model.Mode = modes[0]
		}
	}
	_, providerKey, err := saved.Provider(provider)
	if err != nil {
		return ProviderDefaultView{}, err
	}
	return ProviderDefaultView{
		Mode: string(model.Mode), Model: model.Name, Effort: string(model.Effort), Configured: providerKey != "",
	}, nil
}

// globalDefaultModel expands the ai.defaultModel compact selector. An unset key
// yields the zero model, which contributes nothing.
func globalDefaultModel(saved captainconfig.AIDefaults) (registry.Model, error) {
	name := strings.TrimSpace(saved.DefaultModel)
	if name == "" {
		return registry.Model{}, nil
	}
	model, err := (registry.Model{Name: name}).Expand()
	if err != nil {
		return registry.Model{}, fmt.Errorf("invalid ai.defaultModel %q in captain config: %w", name, err)
	}
	if p, _, ok := registry.ProviderForToken(model.Name); ok {
		model.Provider = p
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
