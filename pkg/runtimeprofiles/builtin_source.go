package runtimeprofiles

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// BuiltinSourceID is the stable source id of the embedded presets, so a stored
// reference to a built-in id survives across releases.
const BuiltinSourceID = "builtin"

const builtinPresetDir = "builtin/presets"

//go:embed builtin/presets/*.yaml
var builtinPresetFiles embed.FS

// NewBuiltinSource is the read-only source of the presets captain ships (Edit,
// Plan, Read-only). It holds no profiles. A database or file preset with the
// same name shadows a built-in; the catalog, not this source, applies that.
func NewBuiltinSource() Source {
	return builtinSource{info: SourceInfo{
		Kind: SourceBuiltin, ID: BuiltinSourceID, Label: "Built-in", Writable: false, Records: []Kind{KindPreset},
	}}
}

type builtinSource struct{ info SourceInfo }

func (s builtinSource) Info() SourceInfo { return s.info }

func (s builtinSource) Presets() Store[Preset, PresetInput] {
	return builtinPresetStore(s)
}

func (s builtinSource) Profiles() Store[Profile, ProfileInput] { return nil }

type builtinPresetStore struct{ info SourceInfo }

func (s builtinPresetStore) List(ctx context.Context) ([]Preset, error) {
	entries, err := fs.ReadDir(builtinPresetFiles, builtinPresetDir)
	if err != nil {
		return nil, fmt.Errorf("list %s presets: %w", s.info.Label, err)
	}
	presets := make([]Preset, 0, len(entries))
	for _, entry := range entries {
		key := strings.TrimSuffix(entry.Name(), fileExt)
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("%w: %s: embedded file %q is not a valid preset key (%s)", ErrInvalid, s.info.Label, entry.Name(), keyPattern)
		}
		preset, err := s.Get(ctx, key)
		if err != nil {
			return nil, err
		}
		presets = append(presets, preset)
	}
	sortByName(presets)
	return presets, nil
}

func (s builtinPresetStore) Get(_ context.Context, key string) (Preset, error) {
	if !keyPattern.MatchString(key) {
		return Preset{}, s.notFound(key)
	}
	file := path.Join(builtinPresetDir, key+fileExt)
	data, err := builtinPresetFiles.ReadFile(file)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Preset{}, s.notFound(key)
		}
		return Preset{}, fmt.Errorf("read embedded %s: %w", file, err)
	}
	// Embedded files carry no modification time, so UpdatedAt stays zero.
	return decodeRecord[Preset, PresetInput](file, data, recordMeta{
		ID: EncodeID(KindPreset, s.info.ID, key), Key: key, Source: s.info,
	})
}

func (s builtinPresetStore) Create(context.Context, PresetInput) (Preset, error) {
	return Preset{}, s.readOnly()
}

func (s builtinPresetStore) Update(context.Context, string, PresetInput) (Preset, error) {
	return Preset{}, s.readOnly()
}

func (s builtinPresetStore) Delete(context.Context, string) error { return s.readOnly() }

func (s builtinPresetStore) readOnly() error {
	return fmt.Errorf("%w: %s presets ship with captain; create a preset with the same name to override one", ErrReadOnly, s.info.Label)
}

func (s builtinPresetStore) notFound(key string) error {
	return fmt.Errorf("%w: %s %q in %s", ErrNotFound, KindPreset, key, s.info.Label)
}
