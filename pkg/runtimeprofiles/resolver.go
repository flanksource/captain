package runtimeprofiles

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// ErrCatalogUnavailable reports selection against a resolver with no catalog.
var ErrCatalogUnavailable = errors.New("runtime preset catalog is not configured")

// SelectionOrigin identifies which precedence input selected a profile.
type SelectionOrigin string

const (
	SelectionRequested SelectionOrigin = "request"
	SelectionPinned    SelectionOrigin = "pin"
	SelectionDefaulted SelectionOrigin = "default"
)

// SelectionError retains the preset references and the input that selected them.
type SelectionError struct {
	Origin SelectionOrigin
	Ref    string
	Err    error
}

func (e *SelectionError) Error() string {
	return fmt.Sprintf("runtime presets %q selected by %s: %v", e.Ref, e.Origin, e.Err)
}

func (e *SelectionError) Unwrap() error { return e.Err }

// CatalogFactory lazily constructs a runtime profile catalog.
type CatalogFactory func(context.Context) (*Catalog, error)

// Resolver assembles host, preset, surface, and request layers.
type Resolver struct {
	catalog CatalogFactory
}

// NewResolver creates a resolver. A nil catalog supports unprofiled base layers.
func NewResolver(catalog CatalogFactory) *Resolver {
	return &Resolver{catalog: catalog}
}

// ResolveOptions contains the ordered inputs to one layered resolution.
type ResolveOptions struct {
	BaseLayers          []api.SpecLayer
	RequestedPresets    []string
	RequestedPresetsSet bool
	PinnedPresets       []string
	PinnedPresetsSet    bool
	DefaultPresets      []string
	// Deprecated profile fields are retained until the final API-removal
	// release. Any non-empty value emits a warning and contributes no layers.
	RequestedProfile string
	PinnedProfile    string
	DefaultProfile   string
	SurfaceLayers    []api.SpecLayer
	RequestLayers    []api.SpecLayer
	Saved            *captainconfig.AIDefaults
	RequireModel     bool
	Normalize        func(api.Spec) (api.SpecNormalization, error)
}

// ResolveResult retains the selected catalog records and effective spec.
type ResolveResult struct {
	Presets  *PresetResolution
	Profile  *Resolution
	Warnings []string
	Resolved api.ResolvedSpec
}

// LayerResult retains the selected catalog records and unresolved layer stack.
type LayerResult struct {
	Presets  *PresetResolution
	Profile  *Resolution
	Warnings []string
	Layers   []api.SpecLayer
}

// Layers selects ordered presets and assembles their layers without resolving a
// model. Deprecated profile selections warn and otherwise have no effect.
func (r *Resolver) Layers(ctx context.Context, options ResolveOptions) (LayerResult, error) {
	if r == nil {
		return LayerResult{}, fmt.Errorf("runtime preset resolver is required")
	}
	refs, origin := selectPresets(options)
	layers := append([]api.SpecLayer(nil), options.BaseLayers...)
	var presets *PresetResolution
	if len(refs) > 0 {
		selection := strings.Join(refs, ",")
		if r.catalog == nil {
			return LayerResult{}, &SelectionError{Origin: origin, Ref: selection, Err: ErrCatalogUnavailable}
		}
		catalog, err := r.catalog(ctx)
		if err != nil {
			return LayerResult{}, &SelectionError{Origin: origin, Ref: selection, Err: err}
		}
		if catalog == nil {
			return LayerResult{}, &SelectionError{Origin: origin, Ref: selection, Err: ErrCatalogUnavailable}
		}
		resolution, err := catalog.PresetLayers(ctx, refs)
		if err != nil {
			return LayerResult{}, &SelectionError{Origin: origin, Ref: selection, Err: err}
		}
		presets = &resolution
		layers = append(layers, resolution.Layers...)
	}
	layers = append(layers, options.SurfaceLayers...)
	layers = append(layers, options.RequestLayers...)
	if err := api.ValidateSpecLayers(layers...); err != nil {
		return LayerResult{}, err
	}
	return LayerResult{Presets: presets, Warnings: profileWarnings(options), Layers: api.OrderSpecLayers(layers...)}, nil
}

// Resolve selects one profile and resolves every layer exactly once.
func (r *Resolver) Resolve(ctx context.Context, options ResolveOptions) (ResolveResult, error) {
	layers, err := r.Layers(ctx, options)
	if err != nil {
		return ResolveResult{}, err
	}
	resolved, err := api.ResolveSpecLayers(api.ResolveSpecOptions{Layers: layers.Layers, Saved: options.Saved, RequireModel: options.RequireModel, Normalize: options.Normalize})
	if err != nil {
		return ResolveResult{}, fmt.Errorf("resolve runtime preset layers: %w", err)
	}
	return ResolveResult{Presets: layers.Presets, Warnings: layers.Warnings, Resolved: resolved}, nil
}

func selectPresets(options ResolveOptions) ([]string, SelectionOrigin) {
	if options.RequestedPresetsSet || options.RequestedPresets != nil {
		return trimPresetRefs(options.RequestedPresets), SelectionRequested
	}
	if options.PinnedPresetsSet || options.PinnedPresets != nil {
		return trimPresetRefs(options.PinnedPresets), SelectionPinned
	}
	if len(options.DefaultPresets) > 0 {
		return trimPresetRefs(options.DefaultPresets), SelectionDefaulted
	}
	return nil, ""
}

func trimPresetRefs(refs []string) []string {
	trimmed := make([]string, len(refs))
	for i, ref := range refs {
		trimmed[i] = strings.TrimSpace(ref)
	}
	return trimmed
}

func profileWarnings(options ResolveOptions) []string {
	if strings.TrimSpace(options.RequestedProfile) != "" || strings.TrimSpace(options.PinnedProfile) != "" || strings.TrimSpace(options.DefaultProfile) != "" {
		return []string{api.RuntimeProfileDeprecationWarning}
	}
	return nil
}
