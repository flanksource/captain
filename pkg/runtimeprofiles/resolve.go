package runtimeprofiles

import (
	"context"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/api"
)

// Layers loads the profile and its presets, canonicalises references to ids,
// and returns authored layers for a host to combine with the rest of a run.
func (c *Catalog) Layers(ctx context.Context, ref string) (Resolution, error) {
	profile, err := c.GetProfile(ctx, ref)
	if err != nil {
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrAmbiguous) {
			return Resolution{}, &OwnedLayersError{Ref: ref, Err: err}
		}
		return Resolution{}, err
	}
	presets := make([]Preset, 0, len(profile.Presets))
	apiPresets := make([]api.RuntimePreset, 0, len(profile.Presets))
	ids := make([]string, 0, len(profile.Presets))
	for _, presetRef := range profile.Presets {
		preset, err := c.GetPreset(ctx, presetRef)
		if err != nil {
			return Resolution{}, &OwnedLayersError{Ref: ref, Err: fmt.Errorf("runtime profile %q references preset %q: %w", profile.Name, presetRef, err)}
		}
		presets = append(presets, preset)
		apiPresets = append(apiPresets, preset.API())
		ids = append(ids, preset.ID)
	}
	profile.Presets = ids
	layers, err := api.RuntimeProfileLayers(api.RuntimeProfileResolveRequest{
		Profile: profile.API(), Presets: apiPresets,
	})
	if err != nil {
		return Resolution{}, &OwnedLayersError{Ref: ref, Err: err}
	}
	return Resolution{Profile: profile, Presets: presets, Layers: layers}, nil
}

// Resolve resolves and validates a profile in isolation for preview.
func (c *Catalog) Resolve(ctx context.Context, ref string) (Resolution, error) {
	resolution, err := c.Layers(ctx, ref)
	if err != nil {
		return Resolution{}, err
	}
	resolved, err := api.ResolveSpecLayers(api.ResolveSpecOptions{Layers: resolution.Layers})
	if err != nil {
		return Resolution{}, err
	}
	resolution.Resolved = resolved
	return resolution, nil
}
