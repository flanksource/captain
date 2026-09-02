package runtimeprofiles

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/api"
)

// Resolve loads the profile, every preset it references (by id or name, from
// any source), canonicalises the references to encoded ids and materialises the
// spec through api.ResolveRuntimeProfile. A reference that resolves nowhere is
// an error naming the profile and the reference, never a silently skipped
// layer.
func (c *Catalog) Resolve(ctx context.Context, ref string) (Resolution, error) {
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
	resolved, err := api.ResolveRuntimeProfile(api.RuntimeProfileResolveRequest{
		Profile: profile.API(), Presets: apiPresets,
	})
	if err != nil {
		return Resolution{}, err
	}
	return Resolution{Profile: profile, Presets: presets, Resolved: resolved}, nil
}
