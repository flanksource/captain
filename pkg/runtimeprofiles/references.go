package runtimeprofiles

import (
	"context"
	"strings"
)

// ReferencedBy lists the profiles naming the preset, by encoded id or by
// case-insensitive name, in catalog order.
func (c *Catalog) ReferencedBy(ctx context.Context, preset Preset) ([]Profile, error) {
	profiles, err := c.ListProfiles(ctx)
	if err != nil {
		return nil, err
	}
	referencing := []Profile{}
	for _, profile := range profiles {
		if references(profile, preset) {
			referencing = append(referencing, profile)
		}
	}
	return referencing, nil
}

func references(profile Profile, preset Preset) bool {
	for _, ref := range profile.Presets {
		ref = strings.TrimSpace(ref)
		if ref == preset.ID || strings.EqualFold(ref, preset.Name) {
			return true
		}
	}
	return false
}

// DeletePreset removes a preset no profile references; otherwise it returns a
// ReferencedError naming the profiles so the caller can report them.
func (c *Catalog) DeletePreset(ctx context.Context, ref string) error {
	preset, err := c.GetPreset(ctx, ref)
	if err != nil {
		return err
	}
	profiles, err := c.ReferencedBy(ctx, preset)
	if err != nil {
		return err
	}
	if len(profiles) > 0 {
		return ReferencedError{Preset: preset, Profiles: profiles}
	}
	return deleteRecord(ctx, c, Source.Presets, KindPreset, preset.meta())
}
