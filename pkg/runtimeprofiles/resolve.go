package runtimeprofiles

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/api"
)

// Layers loads the profile and its presets, canonicalises references to ids,
// and returns authored layers for a host to combine with the rest of a run.
func (c *Catalog) Layers(ctx context.Context, ref string) (Resolution, error) {
	profile, err := c.GetProfile(ctx, ref)
	if err != nil {
		return Resolution{}, err
	}
	presets := make([]Preset, 0, len(profile.Presets))
	apiPresets := make([]api.RuntimePreset, 0, len(profile.Presets))
	ids := make([]string, 0, len(profile.Presets))
	for _, presetRef := range profile.Presets {
		preset, err := c.GetPreset(ctx, presetRef)
		if err != nil {
			return Resolution{}, fmt.Errorf("runtime profile %q references preset %q: %w", profile.Name, presetRef, err)
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
		return Resolution{}, err
	}
	return Resolution{Profile: profile, Presets: presets, Layers: layers}, nil
}

// Resolve resolves and validates a profile in isolation for preview.
func (c *Catalog) Resolve(ctx context.Context, ref string) (Resolution, error) {
	resolution, err := c.Layers(ctx, ref)
	if err != nil {
		return Resolution{}, err
	}
	presets := make([]api.RuntimePreset, 0, len(resolution.Presets))
	for _, preset := range resolution.Presets {
		presets = append(presets, preset.API())
	}
	resolved, err := api.ResolveRuntimeProfile(api.RuntimeProfileResolveRequest{
		Profile: resolution.Profile.API(), Presets: presets,
	})
	if err != nil {
		return Resolution{}, err
	}
	resolution.Resolved = resolved
	return resolution, nil
}
