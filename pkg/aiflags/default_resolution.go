package aiflags

import (
	"fmt"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
)

type DefaultOptions struct {
	Model             registry.Model
	Saved             captainconfig.AIDefaults
	CatalogDefaults   bool
	AllowUnknownModel bool
	// Normalize derives execution context after model selection and before knob defaults.
	// It must preserve selected model names and fallback order.
	Normalize func(registry.Model) (registry.Model, error)
}

type DefaultedModel struct {
	Model        registry.Model
	Sources      map[string]string
	Unconfigured []UnconfiguredCandidate
}

type UnconfiguredCandidate struct {
	Path     string
	Model    string
	Provider *registry.Provider
}

func ApplyDefaults(options DefaultOptions) (DefaultedModel, error) {
	if err := options.Saved.Validate(); err != nil {
		return DefaultedModel{}, err
	}
	model := (registry.Model{}).Merge(options.Model)
	if err := model.ValidateOptions(); err != nil {
		return DefaultedModel{}, err
	}
	global, err := globalDefaultModel(options.Saved)
	if err != nil {
		return DefaultedModel{}, err
	}
	model, err = model.Expand()
	if err != nil {
		return DefaultedModel{}, err
	}
	result := DefaultedModel{Sources: map[string]string{}}
	selection, err := result.selectModels(modelSelectionOptions{DefaultOptions: options, Model: model, Global: global})
	if err != nil {
		return DefaultedModel{}, err
	}
	model = selection.Model
	if options.Normalize != nil {
		model, err = options.Normalize((registry.Model{}).Merge(model))
		if err != nil {
			return DefaultedModel{}, err
		}
		if err := selection.validateNormalized(model); err != nil {
			return DefaultedModel{}, err
		}
	}
	authored := model
	primary := selection.Candidates[0]
	primary.Model = model
	model, err = result.applyCandidate(primary)
	if err != nil {
		return DefaultedModel{}, err
	}
	for i, fallback := range model.Fallbacks {
		candidate := selection.Candidates[i+1]
		fallback = result.inheritPrimary(fallback, authored, model, fmt.Sprintf("/fallbacks/%d", i))
		fallback = result.fillCandidate(fallback, candidate.Selected, candidate)
		candidate.Model = fallback
		model.Fallbacks[i], err = result.applyCandidate(candidate)
		if err != nil {
			return DefaultedModel{}, err
		}
	}
	result.Model = model
	return result, nil
}

func (result *DefaultedModel) fillCatalogEffort(options candidateDefaults) (registry.Model, error) {
	model := options.Model
	if !options.CatalogDefaults || model.Name == "" || model.Fields().Has("/effort") {
		return model, nil
	}
	provider, err := registry.ProviderFor(model.Name)
	if err != nil {
		if options.AllowUnknownModel && registry.IsUnknownModel(err) {
			return model, nil
		}
		return registry.Model{}, err
	}
	mode := model.Mode
	if mode == "" {
		mode = provider.DefaultMode
	}
	id, _ := provider.ResolveExact(mode, model.Name)
	_, effort, known := registry.ModelEfforts(provider, mode, id)
	if !known {
		return model, nil
	}
	if effort == "" {
		if raw, ok := provider.Lookup(id); ok && raw.DefaultEffort != "" {
			if _, err := registry.ResolveEffort(provider, mode, id, raw.DefaultEffort); err != nil {
				return registry.Model{}, err
			}
		}
		return model, nil
	}
	model.Effort = effort
	result.Sources[options.Path+"/effort"] = "registry.models." + id + ".defaultEffort"
	return model, nil
}

func (result *DefaultedModel) inheritPrimary(fallback, authored, primary registry.Model, path string) registry.Model {
	present, inherited := fallback.Fields(), authored.Fields()
	if !present.Has("/temperature") && inherited.Has("/temperature") {
		fallback.Temperature = authored.Temperature
		fallback = fallback.WithExplicit("/temperature")
		result.Sources[path+"/temperature"] = "primary.temperature"
	}
	if !present.Has("/noCache") && inherited.Has("/noCache") {
		fallback.NoCache = authored.NoCache
		fallback = fallback.WithExplicit("/noCache")
		result.Sources[path+"/noCache"] = "primary.noCache"
	}
	primaryProvider, primaryErr := registry.ProviderFor(primary.Name)
	fallbackProvider, fallbackErr := registry.ProviderFor(fallback.Name)
	if !present.Has("/effort") && inherited.Has("/effort") && primaryErr == nil && fallbackErr == nil && primaryProvider == fallbackProvider {
		fallback.Effort = authored.Effort
		fallback = fallback.WithExplicit("/effort")
		result.Sources[path+"/effort"] = "primary.effort"
	}
	return fallback
}

type candidateDefaults struct {
	Model             registry.Model
	Saved             captainconfig.AIDefaults
	Path              string
	CatalogDefaults   bool
	AllowUnknownModel bool
	Provider          *registry.Provider
	Defaults          DefaultedModel
	Selected          DefaultedModel
}

func (result *DefaultedModel) applyCandidate(options candidateDefaults) (registry.Model, error) {
	model := options.Model
	provider := options.Provider
	if provider != nil {
		model = result.fillCandidate(model, options.Defaults, options)
		if model.Mode == "" {
			if modes := provider.Modes(); len(modes) == 1 && !model.Fields().Has("/mode") {
				model.Mode = modes[0]
				result.Sources[options.Path+"/mode"] = "registry.providers." + provider.Name + ".modes"
			} else if model.Name != "" {
				result.Unconfigured = append(result.Unconfigured, UnconfiguredCandidate{Path: options.Path + "/mode", Model: model.Name, Provider: provider})
			}
		}
	}
	options.Model = result.fillGeneration(model, options)
	return result.fillCatalogEffort(options)
}

func (result *DefaultedModel) fillCandidate(model registry.Model, defaults DefaultedModel, options candidateDefaults) registry.Model {
	present := model.Fields()
	if !present.Has("/mode") && defaults.Model.Mode != "" {
		model.Mode = defaults.Model.Mode
		result.Sources[options.Path+"/mode"] = defaults.Sources["/mode"]
	}
	if !present.Has("/effort") && defaults.Model.Effort != "" {
		model.Effort = defaults.Model.Effort
		result.Sources[options.Path+"/effort"] = defaults.Sources["/effort"]
	}
	return model
}

func (result *DefaultedModel) fillGeneration(model registry.Model, options candidateDefaults) registry.Model {
	present, saved := model.Fields(), options.Saved.Fields()
	if !present.Has("/temperature") && saved.Has("/temperature") {
		temperature := options.Saved.Temperature
		model.Temperature = &temperature
		result.Sources[options.Path+"/temperature"] = "ai.temperature"
	}
	if !present.Has("/noCache") && saved.Has("/noCache") {
		model.NoCache = options.Saved.NoCache
		model = model.WithExplicit("/noCache")
		result.Sources[options.Path+"/noCache"] = "ai.noCache"
	}
	return model
}
