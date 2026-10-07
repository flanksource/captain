package runtimeprofiles

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// NewEmbeddedSource is a read-only preset source over the *.yaml files directly
// in dir of fsys: the presets captain ships, or ones a host binary embeds. Its
// records are built-ins: a database or file preset with the same name shadows
// one, and among built-in sources the one registered first wins.
//
// info names the source; ID and Label are required. Kind, Writable and Records
// are fixed by what the source is, so they may be left zero but never set to
// anything else.
func NewEmbeddedSource(fsys fs.FS, dir string, info SourceInfo) (Source, error) {
	if fsys == nil {
		return nil, fmt.Errorf("embedded runtime source %q requires a file system", info.ID)
	}
	if strings.TrimSpace(info.ID) == "" {
		return nil, fmt.Errorf("embedded runtime source %q requires an id", info.Label)
	}
	if strings.TrimSpace(info.Label) == "" {
		return nil, fmt.Errorf("embedded runtime source %q requires a label", info.ID)
	}
	if info.Kind != "" && info.Kind != SourceBuiltin {
		return nil, fmt.Errorf("embedded runtime source %q cannot be of kind %q", info.ID, info.Kind)
	}
	if info.Writable {
		return nil, fmt.Errorf("embedded runtime source %q is read-only", info.ID)
	}
	if info.Records != nil && !slices.Equal(info.Records, []Kind{KindPreset}) {
		return nil, fmt.Errorf("embedded runtime source %q holds only presets, not %v", info.ID, info.Records)
	}
	stat, err := fs.Stat(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("embedded runtime source %q dir %s: %w", info.ID, dir, err)
	}
	if !stat.IsDir() {
		return nil, fmt.Errorf("embedded runtime source %q dir %s is not a directory", info.ID, dir)
	}
	info.Kind, info.Records = SourceBuiltin, []Kind{KindPreset}
	return embeddedSource{store: embeddedPresetStore{fsys: fsys, dir: dir, info: info}}, nil
}

type embeddedSource struct{ store embeddedPresetStore }

func (s embeddedSource) Info() SourceInfo { return s.store.info }

func (s embeddedSource) Presets() Store[Preset, PresetInput] { return s.store }

func (s embeddedSource) Profiles() Store[Profile, ProfileInput] { return nil }

type embeddedPresetStore struct {
	fsys fs.FS
	dir  string
	info SourceInfo
}

// List follows the file source's rules: directories, dotfiles and non-yaml
// files are skipped, and a yaml file whose stem is not a valid key is an error.
func (s embeddedPresetStore) List(ctx context.Context) ([]Preset, error) {
	entries, err := fs.ReadDir(s.fsys, s.dir)
	if err != nil {
		return nil, fmt.Errorf("list %s presets: %w", s.info.Label, err)
	}
	presets := []Preset{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || path.Ext(name) != fileExt {
			continue
		}
		key := strings.TrimSuffix(name, fileExt)
		if !keyPattern.MatchString(key) {
			return nil, fmt.Errorf("%w: %s: embedded file %q is not a valid preset key (%s)", ErrInvalid, s.info.Label, name, keyPattern)
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

func (s embeddedPresetStore) Get(_ context.Context, key string) (Preset, error) {
	if !keyPattern.MatchString(key) {
		return Preset{}, s.notFound(key)
	}
	file := path.Join(s.dir, key+fileExt)
	data, err := fs.ReadFile(s.fsys, file)
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

func (s embeddedPresetStore) Create(context.Context, PresetInput) (Preset, error) {
	return Preset{}, s.readOnly()
}

func (s embeddedPresetStore) Update(context.Context, string, PresetInput) (Preset, error) {
	return Preset{}, s.readOnly()
}

func (s embeddedPresetStore) Delete(context.Context, string) error { return s.readOnly() }

func (s embeddedPresetStore) readOnly() error {
	return fmt.Errorf("%w: %s presets are embedded in the binary; create a preset with the same name to override one", ErrReadOnly, s.info.Label)
}

func (s embeddedPresetStore) notFound(key string) error {
	return fmt.Errorf("%w: %s %q in %s", ErrNotFound, KindPreset, key, s.info.Label)
}
