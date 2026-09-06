package aiflags

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
)

type selectedModels struct {
	Model      registry.Model
	Candidates []candidateDefaults
}

type modelSelectionOptions struct {
	DefaultOptions
	Model  registry.Model
	Global registry.Model
}

func (result *DefaultedModel) selectModels(options modelSelectionOptions) (selectedModels, error) {
	model := options.Model
	primary, err := prepareCandidate(options, "")
	if err != nil {
		return selectedModels{}, err
	}
	present := model.Fields()
	if !present.Has("/model") && primary.Defaults.Model.Name != "" {
		model.Name = primary.Defaults.Model.Name
		result.Sources["/model"] = primary.Defaults.Sources["/model"]
	}
	var selected registry.ModelList
	if !present.Has("/fallbacks") && primary.Defaults.Model.Fields().Has("/fallbacks") {
		selected = primary.Defaults.Model.Fallbacks
		model.Fallbacks = make(registry.ModelList, len(selected))
		result.Sources["/fallbacks"] = primary.Defaults.Sources["/fallbacks"]
		for i, fallback := range selected {
			model.Fallbacks[i] = registry.Model{Name: fallback.Name}
			path := fmt.Sprintf("/fallbacks/%d/model", i)
			result.Sources[path] = primary.Defaults.Sources[path]
		}
	}
	selection := selectedModels{Candidates: []candidateDefaults{primary}}
	for i, fallback := range model.Fallbacks {
		fallback, err = fallback.Expand()
		if err != nil {
			return selectedModels{}, fmt.Errorf("fallback[%d]: %w", i, err)
		}
		path := fmt.Sprintf("/fallbacks/%d", i)
		fallbackOptions := options
		fallbackOptions.Model = fallback
		candidate, err := prepareCandidate(fallbackOptions, path)
		if err != nil {
			return selectedModels{}, err
		}
		if selected != nil {
			candidate.Selected = DefaultedModel{Model: selected[i], Sources: map[string]string{
				"/mode": primary.Defaults.Sources[path+"/mode"], "/effort": primary.Defaults.Sources[path+"/effort"],
			}}
		}
		model.Fallbacks[i] = fallback
		selection.Candidates = append(selection.Candidates, candidate)
	}
	selection.Model = model
	return selection, nil
}

func prepareCandidate(options modelSelectionOptions, path string) (candidateDefaults, error) {
	model := options.Model
	candidate := candidateDefaults{Model: model, Saved: options.Saved, Path: path,
		CatalogDefaults: options.CatalogDefaults, AllowUnknownModel: options.AllowUnknownModel, Provider: model.Provider}
	if model.Name != "" {
		var err error
		candidate.Provider, err = registry.ProviderFor(model.Name)
		if err != nil && (!options.AllowUnknownModel || !registry.IsUnknownModel(err)) {
			return candidateDefaults{}, err
		}
	} else if path == "" && !model.Fields().Has("/model") {
		candidate.Provider = options.Global.Provider
		if candidate.Provider == nil && strings.TrimSpace(options.Saved.DefaultProvider) != "" {
			var ok bool
			candidate.Provider, ok = registry.ProviderByName(options.Saved.DefaultProvider)
			if !ok {
				return candidateDefaults{}, fmt.Errorf("ai.defaultProvider %q is unknown", options.Saved.DefaultProvider)
			}
		}
	}
	if candidate.Provider != nil {
		var err error
		candidate.Defaults, err = savedProviderModel(options.Saved, candidate.Provider, options.Global)
		if err != nil {
			return candidateDefaults{}, err
		}
	}
	return candidate, nil
}

func (selection selectedModels) validateNormalized(model registry.Model) error {
	if model.Name != selection.Model.Name || len(model.Fallbacks) != len(selection.Model.Fallbacks) {
		return fmt.Errorf("model normalization must preserve selected model names and fallback order")
	}
	for i, fallback := range model.Fallbacks {
		if fallback.Name != selection.Model.Fallbacks[i].Name {
			return fmt.Errorf("model normalization must preserve fallback[%d] selection %q", i, selection.Model.Fallbacks[i].Name)
		}
	}
	return model.ValidateOptions()
}
