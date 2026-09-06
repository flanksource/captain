package aiflags

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

func savedProviderModel(saved captainconfig.AIDefaults, provider *registry.Provider, global registry.Model) (DefaultedModel, error) {
	result := DefaultedModel{Sources: map[string]string{}}
	if global.Provider == provider {
		result.Model = (registry.Model{}).Merge(global)
		attributeFields(result.Sources, result.Model, "", "ai.defaultModel")
	}
	configured, providerKey, err := saved.Provider(provider)
	if err != nil {
		return DefaultedModel{}, err
	}
	if providerKey == "" {
		providerKey = provider.Name
	}
	model := registry.Model{Name: strings.TrimSpace(configured.Model), Mode: registry.RuntimeMode(strings.TrimSpace(configured.Mode)), Effort: registry.Effort(strings.TrimSpace(configured.ReasoningEffort))}
	if err := model.ValidateOptions(); err != nil {
		return DefaultedModel{}, fmt.Errorf("ai.providers.%s: %w", providerKey, err)
	}
	compact, err := (registry.Model{Name: model.Name}).Expand()
	if err != nil {
		return DefaultedModel{}, fmt.Errorf("ai.providers.%s.model: %w", providerKey, err)
	}
	model, err = model.Expand()
	if err != nil {
		return DefaultedModel{}, err
	}
	if model.Name != "" {
		actual, err := registry.ProviderFor(model.Name)
		if err != nil || actual != provider {
			return DefaultedModel{}, fmt.Errorf("ai.providers.%s.model %q does not name a model from %s", providerKey, configured.Model, provider.Name)
		}
	}
	result.Model = result.Model.Merge(model)
	for path := range model.Fields() {
		key := strings.TrimPrefix(path, "/")
		if compact.Fields().Has(path) {
			key = "model"
		} else if path == "/effort" {
			key = "reasoningEffort"
		}
		result.Sources[path] = "ai.providers." + providerKey + "." + key
	}
	if len(model.Fallbacks) > 0 {
		attributeFallbacks(result.Sources, model.Fallbacks, "", "ai.providers."+providerKey+".model")
	}
	if result.Model.Mode != "" {
		if _, err := provider.RequireMode(result.Model.Mode); err != nil {
			return DefaultedModel{}, fmt.Errorf("%s: %w", result.Sources["/mode"], err)
		}
	}
	return result, nil
}

func attributeFields(sources map[string]string, model registry.Model, prefix, source string) {
	for path := range model.Fields() {
		sources[prefix+path] = source
	}
	attributeFallbacks(sources, model.Fallbacks, prefix, source)
}

func attributeFallbacks(sources map[string]string, models registry.ModelList, prefix, source string) {
	for i, model := range models {
		attributeFields(sources, model, fmt.Sprintf("%s/fallbacks/%d", prefix, i), source)
	}
}
