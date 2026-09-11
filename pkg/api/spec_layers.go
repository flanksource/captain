package api

import (
	"fmt"
	"slices"
	"strings"
)

// SpecLayerScope identifies one deterministic level in a resolved runtime profile.
type SpecLayerScope string

const (
	SpecLayerGlobal  SpecLayerScope = "global"
	SpecLayerContext SpecLayerScope = "context"
	SpecLayerSurface SpecLayerScope = "surface"
	SpecLayerUser    SpecLayerScope = "user"
)

// SpecLayerSource identifies where a trace row came from: a reusable preset,
// the task-specific profile spec, a .prompt document's frontmatter, or the
// caller's request.
type SpecLayerSource string

const (
	SpecLayerSourcePreset  SpecLayerSource = "preset"
	SpecLayerSourceProfile SpecLayerSource = "profile"
	SpecLayerSourcePrompt  SpecLayerSource = "prompt"
	SpecLayerSourceRequest SpecLayerSource = "request"
)

// SpecLayer is one named source of runtime defaults. A layer only ever
// supplies values: the last layer naming a field owns it, and no layer can
// restrict what a later one selects. A posture that must survive the whole
// stack is applied by its owner as the final layer, not attached to an earlier
// one as a ceiling.
type SpecLayer struct {
	ID     string          `json:"id,omitempty" yaml:"id,omitempty"`
	Source SpecLayerSource `json:"source,omitempty" yaml:"source,omitempty"`
	Name   string          `json:"name" yaml:"name"`
	Scope  SpecLayerScope  `json:"scope" yaml:"scope"`
	Spec   Spec            `json:"spec,omitempty" yaml:"spec,omitempty"`
}

// ResolvedSpec is Captain's effective runtime profile plus ordered provenance.
type ResolvedSpec struct {
	Spec       Spec                       `json:"spec" yaml:"spec"`
	Trace      []SpecLayer                `json:"trace" yaml:"trace"`
	Warnings   []string                   `json:"warnings,omitempty" yaml:"warnings,omitempty"`
	Provenance map[string]FieldProvenance `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

// PromptSpecLayer adapts parsed .prompt frontmatter into the normal surface layer.
func PromptSpecLayer(name string, spec Spec) SpecLayer {
	return SpecLayer{Name: name, Source: SpecLayerSourcePrompt, Scope: SpecLayerSurface, Spec: spec}
}

// RequestSpecLayer adapts a caller's per-run overrides into the user layer, the
// last layer resolved so it wins over every authored default.
func RequestSpecLayer(name string, spec Spec) SpecLayer {
	return SpecLayer{Name: name, Source: SpecLayerSourceRequest, Scope: SpecLayerUser, Spec: spec}
}

// OrderSpecLayers copies the stack into effective scope order, preserving ties.
func OrderSpecLayers(input ...SpecLayer) []SpecLayer {
	layers := append([]SpecLayer(nil), input...)
	slices.SortStableFunc(layers, func(left, right SpecLayer) int {
		return scopeRank(left.Scope) - scopeRank(right.Scope)
	})
	return layers
}

// ComposeSpecLayers overlays raw defaults without resolving a runtime.
func ComposeSpecLayers(options ResolveSpecOptions) (ComposedSpec, error) {
	if err := ValidateSpecLayers(options.Layers...); err != nil {
		return ComposedSpec{}, err
	}
	layers := OrderSpecLayers(options.Layers...)
	resolved := ComposedSpec{Trace: make([]SpecLayer, 0, len(layers)), Provenance: map[string]FieldProvenance{}}
	for _, layer := range layers {
		resolved.recordLayer(layer)
		resolved.Spec = resolved.Spec.Merge(layer.Spec)
		resolved.Trace = append(resolved.Trace, cloneSpecLayer(layer))
	}

	if options.Saved != nil || options.RequireModel || options.Normalize != nil {
		if err := resolved.expandModel(); err != nil {
			return ComposedSpec{}, err
		}
	}
	if err := resolved.applyDefaults(options); err != nil {
		return ComposedSpec{}, err
	}
	if err := resolved.Spec.Budget.Validate(); err != nil {
		return ComposedSpec{}, fmt.Errorf("effective run budget: %w", err)
	}
	return resolved, nil
}

func validateSpecLayer(layer SpecLayer) error {
	if strings.TrimSpace(layer.Name) == "" {
		return fmt.Errorf("spec layer name is required")
	}
	if scopeRank(layer.Scope) < 0 {
		return fmt.Errorf("spec layer %q has invalid scope %q", layer.Name, layer.Scope)
	}
	return nil
}

func scopeRank(scope SpecLayerScope) int {
	switch scope {
	case SpecLayerGlobal:
		return 0
	case SpecLayerContext:
		return 1
	case SpecLayerSurface:
		return 2
	case SpecLayerUser:
		return 3
	default:
		return -1
	}
}

func cloneSpecLayer(layer SpecLayer) SpecLayer {
	layer.Spec = Spec{}.Merge(layer.Spec)
	return layer
}
