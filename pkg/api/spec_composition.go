package api

import "fmt"

// ComposedSpec is an ordered structural projection, not a validated runtime.
type ComposedSpec struct {
	Spec        Spec               `json:"spec" yaml:"spec"`
	Constraints RuntimeConstraints `json:"constraints" yaml:"constraints"`
	Trace       []SpecLayer        `json:"trace" yaml:"trace"`
}

// ResolveSpecLayers validates the effective runtime after composing every layer.
func ResolveSpecLayers(input ...SpecLayer) (ResolvedSpec, error) {
	composed, err := ComposeSpecLayers(input...)
	if err != nil {
		return ResolvedSpec{}, err
	}
	resolved := ResolvedSpec{Spec: composed.Spec, Constraints: composed.Constraints, Trace: composed.Trace}
	if err := resolved.Spec.ValidateStructure(); err != nil {
		return ResolvedSpec{}, fmt.Errorf("effective spec: %w", err)
	}
	if resolved.Spec.Name == "" {
		return resolved, nil
	}
	resolved.Spec.Model, err = ResolveModel(resolved.Spec.Model)
	if err != nil {
		return ResolvedSpec{}, err
	}
	if err := validateResolvedModels(resolved); err != nil {
		return ResolvedSpec{}, err
	}
	resolved.Warnings, err = ValidateRuntimeSpec(resolved.Spec)
	if err != nil {
		return ResolvedSpec{}, err
	}
	return resolved, nil
}

// AllowsModel reports membership in the composed restrictive catalog.
func (composed ComposedSpec) AllowsModel(model Model) bool {
	return allowsModel(composed.Constraints.Models, model)
}
