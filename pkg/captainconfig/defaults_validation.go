package captainconfig

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api/registry"
)

// Validate checks saved declarations before a request can hide invalid defaults.
func (a AIDefaults) Validate() error {
	if math.IsNaN(a.Temperature) || math.IsInf(a.Temperature, 0) || a.Temperature < 0 || a.Temperature > 2 {
		return fmt.Errorf("ai.temperature must be between 0 and 2, got %v", a.Temperature)
	}
	if math.IsNaN(a.BudgetUSD) || math.IsInf(a.BudgetUSD, 0) || a.BudgetUSD < 0 {
		return fmt.Errorf("ai.budgetUSD must be nonnegative, got %v", a.BudgetUSD)
	}
	if a.MaxTokens < 0 {
		return fmt.Errorf("ai.maxTokens must be nonnegative, got %d", a.MaxTokens)
	}
	if a.Timeout != "" {
		if timeout, err := time.ParseDuration(a.Timeout); err != nil || timeout <= 0 {
			return fmt.Errorf("ai.timeout must be a positive duration, got %q", a.Timeout)
		}
	}
	if a.DefaultProvider != "" {
		if _, ok := registry.ProviderByName(strings.TrimSpace(a.DefaultProvider)); !ok {
			return fmt.Errorf("ai.defaultProvider %q is unknown", a.DefaultProvider)
		}
	}
	if err := validateSavedSelector(a.DefaultModel, "ai.defaultModel", nil); err != nil {
		return err
	}
	providers := make([]string, 0, len(a.Providers))
	for name := range a.Providers {
		providers = append(providers, name)
	}
	sort.Strings(providers)
	configured := map[string]string{}
	for _, name := range providers {
		provider, ok := registry.ProviderByName(name)
		if !ok {
			return fmt.Errorf("ai.providers.%s is unknown", name)
		}
		if previous := configured[provider.Name]; previous != "" {
			return fmt.Errorf("ai.providers.%s and ai.providers.%s configure the same provider %s", previous, name, provider.Name)
		}
		configured[provider.Name] = name
		if err := validateProviderDefaults(name, a.Providers[name]); err != nil {
			return err
		}
	}
	return nil
}

// Provider returns one provider's saved defaults and the exact key that
// authored them. Aliases are accepted, while duplicate keys for one provider
// fail because neither value has a well-defined precedence.
func (a AIDefaults) Provider(provider *registry.Provider) (ProviderDefaults, string, error) {
	if provider == nil {
		return ProviderDefaults{}, "", fmt.Errorf("provider is required")
	}
	keys := make([]string, 0, len(a.Providers))
	for key := range a.Providers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var defaults ProviderDefaults
	var source string
	for _, key := range keys {
		configured, ok := registry.ProviderByName(key)
		if !ok {
			return ProviderDefaults{}, "", fmt.Errorf("ai.providers.%s is unknown", key)
		}
		if configured != provider {
			continue
		}
		if source != "" {
			return ProviderDefaults{}, "", fmt.Errorf("ai.providers.%s and ai.providers.%s configure the same provider %s", source, key, provider.Name)
		}
		defaults, source = a.Providers[key], key
	}
	return defaults, source, nil
}

// SetProvider updates a provider through the exact key already authored in the
// saved file. A provider without an existing entry is written by canonical name.
func (a *AIDefaults) SetProvider(provider *registry.Provider, defaults ProviderDefaults) error {
	if a == nil {
		return fmt.Errorf("AI defaults are required")
	}
	_, key, err := a.Provider(provider)
	if err != nil {
		return err
	}
	if key == "" {
		key = provider.Name
	}
	if a.Providers == nil {
		a.Providers = map[string]ProviderDefaults{}
	}
	a.Providers[key] = defaults
	return nil
}

func validateProviderDefaults(name string, defaults ProviderDefaults) error {
	provider, ok := registry.ProviderByName(name)
	if !ok {
		return fmt.Errorf("ai.providers.%s is unknown", name)
	}
	if defaults.Mode != "" {
		if _, err := provider.RequireMode(registry.RuntimeMode(strings.TrimSpace(defaults.Mode))); err != nil {
			return fmt.Errorf("ai.providers.%s.mode: %w", name, err)
		}
	}
	if err := registry.Effort(strings.TrimSpace(defaults.ReasoningEffort)).Validate(); err != nil {
		return fmt.Errorf("ai.providers.%s.reasoningEffort: %w", name, err)
	}
	return validateSavedSelector(defaults.Model, "ai.providers."+name+".model", provider)
}

func validateSavedSelector(value, key string, expected *registry.Provider) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	model, err := (registry.Model{Name: value}).Expand()
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	for i, candidate := range append([]registry.Model{model}, model.Fallbacks...) {
		provider, err := registry.ProviderFor(candidate.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
		if i == 0 && expected != nil && provider != expected {
			return fmt.Errorf("%s %q must select a model from %s", key, value, expected.Name)
		}
		if candidate.Mode != "" {
			if _, err := provider.RequireMode(candidate.Mode); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	return nil
}
