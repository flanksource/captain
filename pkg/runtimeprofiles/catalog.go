package runtimeprofiles

import (
	"context"
	"fmt"
	"strings"
)

// Catalog unifies every source. Reads fan out across sources in registration
// order; writes go to the named target source, or the database when none is
// named. Names are unique case-insensitively across all sources.
type Catalog struct {
	sources []Source
	byID    map[string]Source
}

// NewCatalog registers sources in precedence order. Source ids must be unique:
// they are embedded in record ids, so a repeated id would make two records
// indistinguishable.
func NewCatalog(sources ...Source) (*Catalog, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("runtime catalog requires at least one source")
	}
	catalog := &Catalog{byID: make(map[string]Source, len(sources))}
	for _, source := range sources {
		info := source.Info()
		if strings.TrimSpace(info.ID) == "" {
			return nil, fmt.Errorf("runtime source %q has no id", info.Label)
		}
		if existing, dup := catalog.byID[info.ID]; dup {
			return nil, fmt.Errorf("runtime source id %q is registered twice (%s and %s)",
				info.ID, existing.Info().Label, info.Label)
		}
		catalog.byID[info.ID] = source
		catalog.sources = append(catalog.sources, source)
	}
	return catalog, nil
}

// Sources describes every registered source, so a caller can offer them as
// create targets.
func (c *Catalog) Sources() []SourceInfo {
	infos := make([]SourceInfo, 0, len(c.sources))
	for _, source := range c.sources {
		infos = append(infos, source.Info())
	}
	return infos
}

func (c *Catalog) ListPresets(ctx context.Context) ([]Preset, error) {
	return listAll(ctx, c, Source.Presets)
}

// GetPreset resolves an encoded id or a bare name.
func (c *Catalog) GetPreset(ctx context.Context, ref string) (Preset, error) {
	return get(ctx, c, Source.Presets, KindPreset, ref)
}

// CreatePreset stores a preset in the target source; an empty target means the
// database.
func (c *Catalog) CreatePreset(ctx context.Context, target string, in PresetInput) (Preset, error) {
	return create(ctx, c, Source.Presets, KindPreset, target, in)
}

func (c *Catalog) UpdatePreset(ctx context.Context, ref string, in PresetInput) (Preset, error) {
	return update(ctx, c, Source.Presets, KindPreset, ref, in)
}

func (c *Catalog) ListProfiles(ctx context.Context) ([]Profile, error) {
	return listAll(ctx, c, Source.Profiles)
}

// GetProfile resolves an encoded id or a bare name.
func (c *Catalog) GetProfile(ctx context.Context, ref string) (Profile, error) {
	return get(ctx, c, Source.Profiles, KindProfile, ref)
}

// CreateProfile stores a profile in the target source; an empty target means
// the database.
func (c *Catalog) CreateProfile(ctx context.Context, target string, in ProfileInput) (Profile, error) {
	return create(ctx, c, Source.Profiles, KindProfile, target, in)
}

func (c *Catalog) UpdateProfile(ctx context.Context, ref string, in ProfileInput) (Profile, error) {
	return update(ctx, c, Source.Profiles, KindProfile, ref, in)
}

func (c *Catalog) DeleteProfile(ctx context.Context, ref string) error {
	profile, err := c.GetProfile(ctx, ref)
	if err != nil {
		return err
	}
	return deleteRecord(ctx, c, Source.Profiles, KindProfile, profile.meta())
}

// target picks the source a create lands in. The database is the default
// because it is the one source every host of a shared catalog can see.
func (c *Catalog) target(id string) (Source, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		for _, source := range c.sources {
			if source.Info().Kind == SourceDB {
				return source, nil
			}
		}
		return nil, fmt.Errorf("runtime catalog has no database source; name a target source")
	}
	source, ok := c.byID[id]
	if !ok {
		return nil, fmt.Errorf("unknown runtime source %q", id)
	}
	return source, nil
}

// stores selects one kind's store from a source: Source.Presets or
// Source.Profiles as a method expression.
type stores[R record, I input[R, I]] func(Source) Store[R, I]

func storeIn[R record, I input[R, I]](c *Catalog, pick stores[R, I], kind Kind, sourceID string) (Store[R, I], error) {
	source, ok := c.byID[sourceID]
	if !ok {
		return nil, fmt.Errorf("%w: source %q is not in this catalog", ErrNotFound, sourceID)
	}
	store := pick(source)
	if store == nil {
		return nil, fmt.Errorf("runtime source %q holds no %ss", source.Info().Label, kind)
	}
	return store, nil
}

func listAll[R record, I input[R, I]](ctx context.Context, c *Catalog, pick stores[R, I]) ([]R, error) {
	records := []R{}
	for _, source := range c.sources {
		store := pick(source)
		if store == nil {
			continue
		}
		items, err := store.List(ctx)
		if err != nil {
			return nil, err
		}
		records = append(records, items...)
	}
	return records, nil
}

func get[R record, I input[R, I]](ctx context.Context, c *Catalog, pick stores[R, I], kind Kind, ref string) (R, error) {
	var zero R
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return zero, fmt.Errorf("%w: empty %s reference", ErrNotFound, kind)
	}
	decoded, err := DecodeID(ref)
	if err != nil {
		return findByName(ctx, c, pick, kind, ref)
	}
	if decoded.Kind != kind {
		return zero, fmt.Errorf("%w: %s is a %s id, not a %s", ErrNotFound, ref, decoded.Kind, kind)
	}
	store, err := storeIn(c, pick, kind, decoded.SourceID)
	if err != nil {
		return zero, err
	}
	return store.Get(ctx, decoded.Key)
}

// findByName matches a bare name case-insensitively across every source. One
// match resolves; none is not found; several is ambiguous, and the caller must
// use an id.
func findByName[R record, I input[R, I]](ctx context.Context, c *Catalog, pick stores[R, I], kind Kind, name string) (R, error) {
	var zero R
	all, err := listAll(ctx, c, pick)
	if err != nil {
		return zero, err
	}
	var matches []R
	for _, item := range all {
		if strings.EqualFold(item.meta().Name, name) {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 0:
		return zero, fmt.Errorf("%w: %s %q", ErrNotFound, kind, name)
	case 1:
		return matches[0], nil
	}
	locations := make([]string, 0, len(matches))
	for _, match := range matches {
		meta := match.meta()
		locations = append(locations, meta.Source.Label+":"+meta.Key)
	}
	return zero, fmt.Errorf("%w: %s %q matches %s; use an id", ErrAmbiguous, kind, name, strings.Join(locations, ", "))
}

// requireNameFree enforces case-insensitive uniqueness across sources, ignoring
// the record identified by exceptID (the one being renamed).
func requireNameFree[R record, I input[R, I]](ctx context.Context, c *Catalog, pick stores[R, I], kind Kind, name, exceptID string) error {
	all, err := listAll(ctx, c, pick)
	if err != nil {
		return err
	}
	for _, item := range all {
		meta := item.meta()
		if meta.ID != exceptID && strings.EqualFold(meta.Name, name) {
			return fmt.Errorf("%w: %s %q already exists in %s", ErrNameTaken, kind, meta.Name, meta.Source.Label)
		}
	}
	return nil
}

func create[R record, I input[R, I]](ctx context.Context, c *Catalog, pick stores[R, I], kind Kind, target string, in I) (R, error) {
	var zero R
	in = in.trimmed()
	if err := in.validate(); err != nil {
		return zero, err
	}
	source, err := c.target(target)
	if err != nil {
		return zero, err
	}
	store, err := storeIn(c, pick, kind, source.Info().ID)
	if err != nil {
		return zero, err
	}
	if !source.Info().Writable {
		return zero, fmt.Errorf("%w: %s", ErrReadOnly, source.Info().Label)
	}
	if err := requireNameFree(ctx, c, pick, kind, in.name(), ""); err != nil {
		return zero, err
	}
	return store.Create(ctx, in)
}

func update[R record, I input[R, I]](ctx context.Context, c *Catalog, pick stores[R, I], kind Kind, ref string, in I) (R, error) {
	var zero R
	in = in.trimmed()
	if err := in.validate(); err != nil {
		return zero, err
	}
	current, err := get(ctx, c, pick, kind, ref)
	if err != nil {
		return zero, err
	}
	meta := current.meta()
	if !meta.Source.Writable {
		return zero, fmt.Errorf("%w: %s", ErrReadOnly, meta.Source.Label)
	}
	if !strings.EqualFold(meta.Name, in.name()) {
		if err := requireNameFree(ctx, c, pick, kind, in.name(), meta.ID); err != nil {
			return zero, err
		}
	}
	store, err := storeIn(c, pick, kind, meta.Source.ID)
	if err != nil {
		return zero, err
	}
	return store.Update(ctx, meta.Key, in)
}

func deleteRecord[R record, I input[R, I]](ctx context.Context, c *Catalog, pick stores[R, I], kind Kind, meta recordMeta) error {
	if !meta.Source.Writable {
		return fmt.Errorf("%w: %s", ErrReadOnly, meta.Source.Label)
	}
	store, err := storeIn(c, pick, kind, meta.Source.ID)
	if err != nil {
		return err
	}
	return store.Delete(ctx, meta.Key)
}
