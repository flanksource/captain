package runtimeprofiles

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

// PresetLayers loads a flat ordered preset selection and canonicalises every
// reference to its catalog id. Nested references are persisted but deliberately
// refused by api.RuntimePresetLayers until Gavel TODO 3178c771 is implemented.
func (c *Catalog) PresetLayers(ctx context.Context, refs []string) (PresetResolution, error) {
	presets := make([]Preset, 0, len(refs))
	apiPresets := make([]api.RuntimePreset, 0, len(refs))
	selected := make([]string, 0, len(refs))
	for _, ref := range refs {
		preset, err := c.GetPreset(ctx, ref)
		if err != nil {
			if errors.Is(err, ErrNotFound) || errors.Is(err, ErrAmbiguous) {
				return PresetResolution{}, err
			}
			return PresetResolution{}, &OwnedLayersError{Kind: KindPreset, Ref: strings.Join(refs, ","), Err: fmt.Errorf("runtime preset selection references %q: %w", ref, err)}
		}
		presets = append(presets, preset)
		apiPresets = append(apiPresets, preset.API())
		selected = append(selected, preset.ID)
	}
	layers, err := api.RuntimePresetLayers(api.RuntimePresetResolveRequest{Selected: selected, Presets: apiPresets})
	if err != nil {
		return PresetResolution{}, &OwnedLayersError{Kind: KindPreset, Ref: strings.Join(refs, ","), Err: err}
	}
	return PresetResolution{Presets: presets, Layers: layers}, nil
}

func (c *Catalog) ResolvePresets(ctx context.Context, refs []string) (PresetResolution, error) {
	resolution, err := c.PresetLayers(ctx, refs)
	if err != nil {
		return PresetResolution{}, err
	}
	resolved, err := api.ResolveSpecLayers(api.ResolveSpecOptions{Layers: resolution.Layers})
	if err != nil {
		return PresetResolution{}, err
	}
	resolution.Resolved = resolved
	return resolution, nil
}

// Layers loads the profile and its presets, canonicalises references to ids,
// and returns authored layers for a host to combine with the rest of a run.
func (c *Catalog) Layers(ctx context.Context, ref string) (Resolution, error) {
	profile, err := c.GetProfile(ctx, ref)
	if err != nil {
		if !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrAmbiguous) {
			return Resolution{}, &OwnedLayersError{Kind: KindProfile, Ref: ref, Err: err}
		}
		return Resolution{}, err
	}
	presets := make([]Preset, 0, len(profile.Presets))
	apiPresets := make([]api.RuntimePreset, 0, len(profile.Presets))
	ids := make([]string, 0, len(profile.Presets))
	for _, presetRef := range profile.Presets {
		preset, err := c.GetPreset(ctx, presetRef)
		if err != nil {
			return Resolution{}, &OwnedLayersError{Kind: KindProfile, Ref: ref, Err: fmt.Errorf("runtime profile %q references preset %q: %w", profile.Name, presetRef, err)}
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
		return Resolution{}, &OwnedLayersError{Kind: KindProfile, Ref: ref, Err: err}
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
