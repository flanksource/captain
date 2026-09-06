package api

import (
	"fmt"

	"github.com/flanksource/captain/pkg/captainconfig"
)

// ResolveSpecOptions supplies a complete authored stack and optional immutable
// defaults. Nil Saved performs no saved or built-in default injection.
type ResolveSpecOptions struct {
	Layers       []SpecLayer
	Saved        *captainconfig.AIDefaults
	RequireModel bool
	Normalize    func(Spec) (SpecNormalization, error)
}

// SpecNormalization declares context-derived values without adding an authored
// trace layer. Fields identifies only the paths derived by the callback.
type SpecNormalization struct {
	Spec   Spec
	Fields FieldPresence
	Source FieldSource
}

// FieldSourceKind distinguishes authored values, saved settings, and catalog facts.
type FieldSourceKind string

const (
	FieldSourceLayer   FieldSourceKind = "layer"
	FieldSourceSaved   FieldSourceKind = "saved"
	FieldSourceCatalog FieldSourceKind = "catalog"
	FieldSourceContext FieldSourceKind = "context"
)

// FieldSource names a serialized field's owner and its original source key.
type FieldSource struct {
	Kind    FieldSourceKind `json:"kind" yaml:"kind"`
	Name    string          `json:"name" yaml:"name"`
	Key     string          `json:"key" yaml:"key"`
	LayerID string          `json:"layerId,omitempty" yaml:"layerId,omitempty"`
}

// FieldProvenance retains authorship when a runtime or constraint normalizes it.
type FieldProvenance struct {
	Source       FieldSource  `json:"source" yaml:"source"`
	NormalizedBy *FieldSource `json:"normalizedBy,omitempty" yaml:"normalizedBy,omitempty"`
}

// ComposedSpec is an ordered structural projection, not a validated runtime.
type ComposedSpec struct {
	fieldLayers map[string]int
	Spec        Spec                       `json:"spec" yaml:"spec"`
	Constraints RuntimeConstraints         `json:"constraints" yaml:"constraints"`
	Trace       []SpecLayer                `json:"trace" yaml:"trace"`
	Provenance  map[string]FieldProvenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
	Warnings    []string                   `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// ResolveSpecLayers validates the effective runtime after composing every layer.
func ResolveSpecLayers(options ResolveSpecOptions) (ResolvedSpec, error) {
	composed, err := ComposeSpecLayers(options)
	if err != nil {
		return ResolvedSpec{}, err
	}
	if options.Saved == nil && !options.RequireModel && options.Normalize == nil {
		if err := composed.expandModel(); err != nil {
			return ResolvedSpec{}, err
		}
	}
	resolved := ResolvedSpec{Spec: composed.Spec, Constraints: composed.Constraints, Trace: composed.Trace,
		Provenance: composed.Provenance, Warnings: composed.Warnings}
	if err := resolved.Spec.ValidateStructure(); err != nil {
		return ResolvedSpec{}, fmt.Errorf("effective spec: %w", err)
	}
	if resolved.Spec.Sandbox != nil {
		if err := resolved.Spec.Sandbox.Validate(); err != nil {
			return ResolvedSpec{}, fmt.Errorf("effective sandbox: %w", err)
		}
	}
	if resolved.Spec.Name == "" {
		return resolved, nil
	}
	before := resolved.Spec.Model
	resolved.Spec.Model, err = ResolveModel(resolved.Spec.Model)
	if err != nil {
		return ResolvedSpec{}, err
	}
	if err := validateResolvedModels(resolved); err != nil {
		return ResolvedSpec{}, err
	}
	resolved.recordNormalization(before)
	warnings, err := ValidateRuntimeSpec(resolved.Spec)
	if err != nil {
		return ResolvedSpec{}, err
	}
	resolved.Warnings = append(resolved.Warnings, warnings...)
	return resolved, nil
}

// AllowsModel reports membership in the composed restrictive catalog.
func (composed ComposedSpec) AllowsModel(model Model) bool {
	return allowsModel(composed.Constraints.Models, model)
}
