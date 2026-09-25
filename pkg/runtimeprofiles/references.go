package runtimeprofiles

import (
	"context"
	"fmt"
	"slices"
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
// ReferencedError naming the profiles so the caller can report them. A
// read-only preset is refused before references are counted. Deleting a preset
// that overrides a built-in hands its name back to the built-in, so only
// references by id block it. A built-in id is refused even when an override
// answers reads for it; delete the override through its own id or name.
func (c *Catalog) DeletePreset(ctx context.Context, ref string) error {
	preset, err := getForWrite(ctx, c, Source.Presets, KindPreset, ref)
	if err != nil {
		return err
	}
	if !preset.Source.Writable {
		return fmt.Errorf("%w: %s", ErrReadOnly, preset.Source.Label)
	}
	profiles, err := c.ReferencedBy(ctx, preset)
	if err != nil {
		return err
	}
	overridesBuiltin, err := c.hasBuiltinPreset(ctx, preset.Name)
	if err != nil {
		return err
	}
	if overridesBuiltin {
		profiles = slices.DeleteFunc(profiles, func(profile Profile) bool {
			return !slices.ContainsFunc(profile.Presets, func(ref string) bool { return strings.TrimSpace(ref) == preset.ID })
		})
	}
	if len(profiles) > 0 {
		return ReferencedError{Preset: preset, Profiles: profiles}
	}
	return deleteRecord(ctx, c, Source.Presets, KindPreset, preset.meta())
}

// hasBuiltinPreset reports whether a built-in source ships a preset with the
// name, whether or not another record currently shadows it.
func (c *Catalog) hasBuiltinPreset(ctx context.Context, name string) (bool, error) {
	for _, source := range c.sources {
		if source.Info().Kind != SourceBuiltin {
			continue
		}
		store := source.Presets()
		if store == nil {
			continue
		}
		presets, err := store.List(ctx)
		if err != nil {
			return false, err
		}
		if slices.ContainsFunc(presets, func(builtin Preset) bool { return strings.EqualFold(builtin.Name, name) }) {
			return true, nil
		}
	}
	return false, nil
}
