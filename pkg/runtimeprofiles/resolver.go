package runtimeprofiles

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

// ErrCatalogUnavailable reports selection against a resolver with no catalog.
var ErrCatalogUnavailable = errors.New("runtime profile catalog is not configured")

// SelectionOrigin identifies which precedence input selected a profile.
type SelectionOrigin string

const (
	SelectionRequested SelectionOrigin = "request"
	SelectionPinned    SelectionOrigin = "pin"
	SelectionDefaulted SelectionOrigin = "default"
)

// SelectionError retains the profile reference and the input that selected it.
type SelectionError struct {
	Origin SelectionOrigin
	Ref    string
	Err    error
}

func (e *SelectionError) Error() string {
	return fmt.Sprintf("runtime profile %q selected by %s: %v", e.Ref, e.Origin, e.Err)
}

func (e *SelectionError) Unwrap() error { return e.Err }

// CatalogFactory lazily constructs a runtime profile catalog.
type CatalogFactory func(context.Context) (*Catalog, error)

// Resolver assembles host, profile, surface, and request layers.
type Resolver struct {
	catalog CatalogFactory
}

// NewResolver creates a resolver. A nil catalog supports unprofiled base layers.
func NewResolver(catalog CatalogFactory) *Resolver {
	return &Resolver{catalog: catalog}
}

// ResolveOptions contains the ordered inputs to one layered resolution.
type ResolveOptions struct {
	BaseLayers       []api.SpecLayer
	RequestedProfile string
	PinnedProfile    string
	DefaultProfile   string
	SurfaceLayers    []api.SpecLayer
	RequestLayers    []api.SpecLayer
}

// ResolveResult retains the selected catalog records and effective spec.
type ResolveResult struct {
	Profile  *Resolution
	Resolved api.ResolvedSpec
}

// LayerResult retains the selected catalog records and unresolved layer stack.
type LayerResult struct {
	Profile *Resolution
	Layers  []api.SpecLayer
}

// Layers selects one profile and assembles its layers without resolving a model.
func (r *Resolver) Layers(ctx context.Context, options ResolveOptions) (LayerResult, error) {
	if r == nil {
		return LayerResult{}, fmt.Errorf("runtime profile resolver is required")
	}
	ref, origin := selectProfile(options)
	layers := append([]api.SpecLayer(nil), options.BaseLayers...)
	var profile *Resolution
	if ref != "" {
		if r.catalog == nil {
			return LayerResult{}, &SelectionError{Origin: origin, Ref: ref, Err: ErrCatalogUnavailable}
		}
		catalog, err := r.catalog(ctx)
		if err != nil {
			return LayerResult{}, &SelectionError{Origin: origin, Ref: ref, Err: err}
		}
		if catalog == nil {
			return LayerResult{}, &SelectionError{Origin: origin, Ref: ref, Err: ErrCatalogUnavailable}
		}
		resolution, err := catalog.Layers(ctx, ref)
		if err != nil {
			return LayerResult{}, &SelectionError{Origin: origin, Ref: ref, Err: err}
		}
		profile = &resolution
		layers = append(layers, resolution.Layers...)
	}
	layers = append(layers, options.SurfaceLayers...)
	layers = append(layers, options.RequestLayers...)
	if err := api.ValidateSpecLayers(layers...); err != nil {
		return LayerResult{}, err
	}
	return LayerResult{Profile: profile, Layers: api.OrderSpecLayers(layers...)}, nil
}

// Resolve selects one profile and resolves every layer exactly once.
func (r *Resolver) Resolve(ctx context.Context, options ResolveOptions) (ResolveResult, error) {
	layers, err := r.Layers(ctx, options)
	if err != nil {
		return ResolveResult{}, err
	}
	resolved, err := api.ResolveSpecLayers(layers.Layers...)
	if err != nil {
		return ResolveResult{}, fmt.Errorf("resolve runtime profile layers: %w", err)
	}
	return ResolveResult{Profile: layers.Profile, Resolved: resolved}, nil
}

func selectProfile(options ResolveOptions) (string, SelectionOrigin) {
	if ref := strings.TrimSpace(options.RequestedProfile); ref != "" {
		return ref, SelectionRequested
	}
	if ref := strings.TrimSpace(options.PinnedProfile); ref != "" {
		return ref, SelectionPinned
	}
	if ref := strings.TrimSpace(options.DefaultProfile); ref != "" {
		return ref, SelectionDefaulted
	}
	return "", ""
}
