package runtimeprofiles

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// Kind is the record kind a source holds or an id names.
type Kind string

const (
	KindPreset  Kind = "preset"
	KindProfile Kind = "profile"
)

// SourceKind distinguishes the database from a directory of YAML files.
type SourceKind string

const (
	SourceDB   SourceKind = "db"
	SourceFile SourceKind = "file"
)

// SourceInfo describes one catalog source as a destination picker sees it.
// Records lists the kinds the source holds: the database holds both, a file
// source holds the one kind its directory is for.
type SourceInfo struct {
	Kind     SourceKind `json:"kind"`
	ID       string     `json:"id"`
	Label    string     `json:"label"`
	Root     string     `json:"root,omitempty"`
	Writable bool       `json:"writable"`
	Implicit bool       `json:"implicit,omitempty"`
	Records  []Kind     `json:"records"`
}

// Holds reports whether the source stores records of the given kind.
func (info SourceInfo) Holds(kind Kind) bool { return slices.Contains(info.Records, kind) }

// Preset is one reusable runtime layer with its catalog identity.
type Preset struct {
	ID          string                `json:"id"`
	Key         string                `json:"key"`
	Source      SourceInfo            `json:"source"`
	Name        string                `json:"name"`
	Description string                `json:"description,omitempty"`
	Scope       api.SpecLayerScope    `json:"scope"`
	Spec        api.RuntimePresetSpec `json:"spec"`
	UpdatedAt   time.Time             `json:"updatedAt"`
}

// API projects the preset onto the resolver's input type.
func (p Preset) API() api.RuntimePreset {
	return api.RuntimePreset{ID: p.ID, Name: p.Name, Description: p.Description, Scope: p.Scope, Spec: p.Spec}
}

// Profile is a task-specific spec plus the ordered preset references (ids or
// names) layered beneath it.
type Profile struct {
	ID          string     `json:"id"`
	Key         string     `json:"key"`
	Source      SourceInfo `json:"source"`
	Name        string     `json:"name"`
	Description string     `json:"description,omitempty"`
	Spec        api.Spec   `json:"spec"`
	Presets     []string   `json:"presets"`
	UpdatedAt   time.Time  `json:"updatedAt"`
}

// API projects the profile onto the resolver's input type.
func (p Profile) API() api.RuntimeProfile {
	return api.RuntimeProfile{
		ID: p.ID, Name: p.Name, Description: p.Description, Spec: p.Spec, Presets: slices.Clone(p.Presets),
	}
}

// PresetInput is everything a caller authors for a preset. Its YAML shape is
// the file format: the API JSON minus id, key, source and updatedAt.
type PresetInput struct {
	Name        string                `json:"name" yaml:"name"`
	Description string                `json:"description,omitempty" yaml:"description,omitempty"`
	Scope       api.SpecLayerScope    `json:"scope" yaml:"scope"`
	Spec        api.RuntimePresetSpec `json:"spec,omitempty" yaml:"spec,omitempty"`
}

// ProfileInput is everything a caller authors for a profile; see PresetInput.
type ProfileInput struct {
	Name        string   `json:"name" yaml:"name"`
	Description string   `json:"description,omitempty" yaml:"description,omitempty"`
	Spec        api.Spec `json:"spec,omitempty" yaml:"spec,omitempty"`
	Presets     []string `json:"presets,omitempty" yaml:"presets,omitempty"`
}

// Resolution is a profile materialised through the catalog: the profile with
// its preset references canonicalised to ids, the presets and layers in reference
// order, and the effective spec populated only by Catalog.Resolve for preview.
type Resolution struct {
	Profile  Profile          `json:"profile"`
	Presets  []Preset         `json:"presets"`
	Layers   []api.SpecLayer  `json:"-"`
	Resolved api.ResolvedSpec `json:"resolved"`
}

var (
	ErrNotFound  = errors.New("runtime record not found")
	ErrAmbiguous = errors.New("runtime record name is ambiguous")
	ErrNameTaken = errors.New("runtime record name is already taken")
	ErrReadOnly  = errors.New("runtime source is read-only")
	ErrInvalid   = errors.New("invalid runtime record")
)

// ReferencedError refuses to delete a preset that profiles still name.
type ReferencedError struct {
	Preset   Preset
	Profiles []Profile
}

func (e ReferencedError) Error() string {
	names := make([]string, 0, len(e.Profiles))
	for _, profile := range e.Profiles {
		names = append(names, profile.Name)
	}
	return fmt.Sprintf("runtime preset %q is used by %s", e.Preset.Name, strings.Join(names, ", "))
}

// recordMeta is the identity a source stamps on a record. Name is carried so
// the catalog can match bare names without knowing the record type.
type recordMeta struct {
	ID        string
	Key       string
	Name      string
	Source    SourceInfo
	UpdatedAt time.Time
}

type record interface {
	meta() recordMeta
}

func (p Preset) meta() recordMeta {
	return recordMeta{ID: p.ID, Key: p.Key, Name: p.Name, Source: p.Source, UpdatedAt: p.UpdatedAt}
}

func (p Profile) meta() recordMeta {
	return recordMeta{ID: p.ID, Key: p.Key, Name: p.Name, Source: p.Source, UpdatedAt: p.UpdatedAt}
}

// input is what the generic store and catalog code needs from PresetInput and
// ProfileInput: a trimmed copy, validation, and the record it becomes once a
// source has assigned its identity.
type input[R record, I any] interface {
	name() string
	validate() error
	trimmed() I
	build(meta recordMeta) R
}

func (in PresetInput) name() string { return strings.TrimSpace(in.Name) }

func (in PresetInput) trimmed() PresetInput {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	return in
}

func (in PresetInput) validate() error {
	name := in.name()
	if name == "" {
		return fmt.Errorf("%w: preset name is required", ErrInvalid)
	}
	preset := api.RuntimePreset{ID: name, Name: name, Scope: in.Scope, Spec: in.Spec}
	if err := api.ValidateRuntimePreset(preset); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

func (in PresetInput) build(meta recordMeta) Preset {
	return Preset{
		ID: meta.ID, Key: meta.Key, Source: meta.Source, Name: in.Name, Description: in.Description,
		Scope: in.Scope, Spec: in.Spec, UpdatedAt: meta.UpdatedAt,
	}
}

func (in ProfileInput) name() string { return strings.TrimSpace(in.Name) }

func (in ProfileInput) trimmed() ProfileInput {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	presets := make([]string, 0, len(in.Presets))
	for _, ref := range in.Presets {
		presets = append(presets, strings.TrimSpace(ref))
	}
	in.Presets = presets
	return in
}

func (in ProfileInput) validate() error {
	name := in.name()
	if name == "" {
		return fmt.Errorf("%w: profile name is required", ErrInvalid)
	}
	for index, ref := range in.Presets {
		if strings.TrimSpace(ref) == "" {
			return fmt.Errorf("%w: profile %q preset reference %d is blank", ErrInvalid, name, index)
		}
	}
	if err := api.ValidateSpecLayers(api.SpecLayer{Name: name + " run spec", Scope: api.SpecLayerSurface,
		Source: api.SpecLayerSourceProfile, Spec: in.Spec}); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return nil
}

func (in ProfileInput) build(meta recordMeta) Profile {
	presets := make([]string, 0, len(in.Presets))
	presets = append(presets, in.Presets...)
	return Profile{
		ID: meta.ID, Key: meta.Key, Source: meta.Source, Name: in.Name, Description: in.Description,
		Spec: in.Spec, Presets: presets, UpdatedAt: meta.UpdatedAt,
	}
}
