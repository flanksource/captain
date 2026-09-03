package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// ProviderDefaultView is aiflags' projection re-exported: one provider's saved
// mode/model/effort. It lives there because pkg/aiflags owns default resolution
// for every caller, not just the CLI.
type ProviderDefaultView = aiflags.ProviderDefaultView

// effectiveProviderDefaults resolves a provider's saved defaults and then
// degrades any selection the user has switched off. aiflags answers what is
// configured; the opt-out set is applied here, where the CLI can also explain
// it (see the whoami disable card).
func effectiveProviderDefaults(saved captainconfig.AIDefaults, provider *api.ModelProvider) (ProviderDefaultView, error) {
	view, err := aiflags.EffectiveDefaults(saved, provider)
	if err != nil {
		return ProviderDefaultView{}, err
	}
	disabled := ai.Disabled()
	mode := api.RuntimeMode(view.Mode)
	if disabled.Runtime(provider, mode) {
		mode = firstEnabledMode(provider, mode)
	}
	model := strings.TrimSpace(view.Model)
	if model == "" {
		model = defaultModelFor(provider, mode)
	}
	if disabled.Model(provider, mode, model) {
		model = firstEnabledModel(provider, mode)
	}
	effort := api.Effort(strings.TrimSpace(view.Effort))
	if disabled.Effort(effort) {
		degraded, err := ai.ResolveModelEffort(provider, mode, model, effort)
		if err != nil {
			return ProviderDefaultView{}, err
		}
		effort = degraded
	}
	view.Mode, view.Model, view.Effort = string(mode), model, string(effort)
	return view, nil
}

// savedProviderDefaults is effectiveProviderDefaults' run-path sibling: the same
// opt-out degradation, but over what the user actually configured rather than
// over registry-seeded values, and it never invents a model for an empty slot.
//
// The distinction is the point. effectiveProviderDefaults fills gaps from
// Provider.DefaultMode and the DefaultModelFor table, which is right for seeding
// `captain configure` and wrong for deciding what a run executes on — an unset
// field must survive as unset so ResolveForRun can refuse it.
func savedProviderDefaults(saved captainconfig.AIDefaults, provider *api.ModelProvider) (ProviderDefaultView, error) {
	view, err := aiflags.SavedDefaults(saved, provider)
	if err != nil {
		return ProviderDefaultView{}, err
	}
	disabled := ai.Disabled()
	mode := api.RuntimeMode(strings.TrimSpace(view.Mode))
	if mode != "" && disabled.Runtime(provider, mode) {
		mode = firstEnabledMode(provider, mode)
	}
	model := strings.TrimSpace(view.Model)
	if model != "" && disabled.Model(provider, mode, model) {
		model = firstEnabledModel(provider, mode)
	}
	effort := api.Effort(strings.TrimSpace(view.Effort))
	if effort != api.EffortNone && disabled.Effort(effort) {
		degraded, err := ai.ResolveModelEffort(provider, mode, model, effort)
		if err != nil {
			return ProviderDefaultView{}, err
		}
		effort = degraded
	}
	view.Mode, view.Model, view.Effort = string(mode), model, string(effort)
	return view, nil
}

// firstEnabledMode replaces a disabled mode with another of the same provider
// that is still enabled. When every one is off it returns the original so the
// view still names what the user configured — the whoami disable card, not this
// projection, is where that state is meant to be visible.
func firstEnabledMode(provider *api.ModelProvider, mode api.RuntimeMode) api.RuntimeMode {
	disabled := ai.Disabled()
	for _, candidate := range provider.Modes() {
		if !disabled.Runtime(provider, candidate) {
			return candidate
		}
	}
	return mode
}

// firstEnabledModel is the runtime's top catalog pick that survives the opt-out
// set. RegistryModelDefs already drops disabled models, so an empty result means
// the whole runtime has been switched off and the seed default stands in.
func firstEnabledModel(provider *api.ModelProvider, mode api.RuntimeMode) string {
	if models := ai.RegistryModelDefs(provider, mode); len(models) > 0 {
		return models[0].ID
	}
	return defaultModelFor(provider, mode)
}

func applyProviderDefaults(model api.Model, saved captainconfig.AIDefaults) (api.Model, error) {
	var err error
	if strings.TrimSpace(model.Name) != "" {
		model, err = model.Expand()
		if err != nil {
			return api.Model{}, err
		}
	}
	model, err = applyCandidateDefaults(model, saved, true)
	if err != nil {
		return api.Model{}, err
	}
	for i := range model.Fallbacks {
		fallback := model.Fallbacks[i]
		fallback, err = fallback.Expand()
		if err != nil {
			return api.Model{}, fmt.Errorf("fallback[%d]: %w", i, err)
		}
		fallback, err = applyCandidateDefaults(fallback, saved, false)
		if err != nil {
			return api.Model{}, fmt.Errorf("fallback[%d]: %w", i, err)
		}
		model.Fallbacks[i] = fallback
	}
	return model, nil
}

func applyCandidateDefaults(model api.Model, saved captainconfig.AIDefaults, allowActive bool) (api.Model, error) {
	provider := model.Provider
	if provider == nil && strings.TrimSpace(model.Name) != "" {
		inferred, err := api.ProviderFor(model.Name)
		if err != nil {
			return api.Model{}, err
		}
		provider = inferred
	}
	if provider == nil && allowActive {
		provider, _ = api.ProviderByName(saved.ActiveProvider())
	}
	if provider == nil {
		return api.Model{}, fmt.Errorf("provider cannot be resolved for model %q", model.Name)
	}
	// Saved, not effective: this is the run path. Seeding from the registry's
	// built-in tables here is how `captain ai prompt` kept silently defaulting
	// after the flag path stopped.
	defaults, err := savedProviderDefaults(saved, provider)
	if err != nil {
		return api.Model{}, err
	}
	if model.Mode == "" {
		model.Mode = api.RuntimeMode(defaults.Mode)
	}
	if model.Provider == nil {
		model.Provider = provider
	}
	if strings.TrimSpace(model.Name) == "" {
		model.Name = defaults.Model
	}
	if model.Effort == api.EffortNone {
		model.Effort = api.Effort(defaults.Effort)
	}
	if err := model.Effort.Validate(); err != nil {
		return api.Model{}, err
	}
	return model, nil
}

// configurableProviders lists the provider families a user can configure
// defaults for and toggle off.
func configurableProviders() []*api.ModelProvider { return api.Providers() }

func allProviderDefaults(saved captainconfig.AIDefaults) (map[string]ProviderDefaultView, error) {
	providers := configurableProviders()
	out := make(map[string]ProviderDefaultView, len(providers))
	for _, provider := range providers {
		defaults, err := effectiveProviderDefaults(saved, provider)
		if err != nil {
			return nil, err
		}
		out[provider.Name] = defaults
	}
	return out, nil
}
