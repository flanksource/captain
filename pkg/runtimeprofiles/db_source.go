package runtimeprofiles

import (
	"context"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// DBSourceID is the stable source id of the database, so ids of database
// records survive across processes and hosts sharing one database.
const DBSourceID = "db"

// DBSourceOptions supplies the database handles lazily, so a catalog that is
// only listing files never opens a connection. Write may be nil, which makes
// the source read-only.
type DBSourceOptions struct {
	Read  func(context.Context) (*database.DB, error)
	Write func(context.Context) (*database.DB, error)
}

// NewDBSource exposes the captain_runtime_presets and captain_runtime_profiles
// tables as a catalog source.
func NewDBSource(options DBSourceOptions) (Source, error) {
	if options.Read == nil {
		return nil, errors.New("runtime database source requires a Read opener")
	}
	return &dbSource{options: options, info: SourceInfo{
		Kind: SourceDB, ID: DBSourceID, Label: "Database", Writable: options.Write != nil,
		Records: []Kind{KindPreset, KindProfile},
	}}, nil
}

type dbSource struct {
	options DBSourceOptions
	info    SourceInfo
}

func (s *dbSource) Info() SourceInfo                       { return s.info }
func (s *dbSource) Presets() Store[Preset, PresetInput]    { return dbPresets{s} }
func (s *dbSource) Profiles() Store[Profile, ProfileInput] { return dbProfiles{s} }

func (s *dbSource) write(ctx context.Context) (*database.DB, error) {
	if s.options.Write == nil {
		return nil, fmt.Errorf("%w: %s", ErrReadOnly, s.info.Label)
	}
	return s.options.Write(ctx)
}

func (s *dbSource) preset(row database.RuntimePreset) Preset {
	key := row.ID.String()
	return Preset{
		ID: EncodeID(KindPreset, s.info.ID, key), Key: key, Source: s.info, Name: row.Name,
		Description: row.Description, Scope: row.Scope, Spec: row.Spec, UpdatedAt: row.UpdatedAt,
	}
}

func (s *dbSource) profile(row database.RuntimeProfile) Profile {
	key := row.ID.String()
	return Profile{
		ID: EncodeID(KindProfile, s.info.ID, key), Key: key, Source: s.info, Name: row.Name,
		Description: row.Description, Spec: row.Spec, Presets: row.Presets, UpdatedAt: row.UpdatedAt,
	}
}

// dbKey parses a source-local key. A key that is not a uuid cannot name a row,
// which is a not-found rather than a malformed request.
func dbKey(key string) (uuid.UUID, error) {
	id, err := uuid.Parse(key)
	if err != nil {
		return uuid.Nil, fmt.Errorf("%w: %q is not a database key", ErrNotFound, key)
	}
	return id, nil
}

func mapDBError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, database.ErrRuntimePresetNotFound), errors.Is(err, database.ErrRuntimeProfileNotFound):
		return fmt.Errorf("%w: %w", ErrNotFound, err)
	case errors.Is(err, database.ErrRuntimeNameTaken):
		return fmt.Errorf("%w: %w", ErrNameTaken, err)
	case errors.Is(err, database.ErrRuntimeInvalid):
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return err
}

type dbPresets struct{ *dbSource }

func (s dbPresets) List(ctx context.Context) ([]Preset, error) {
	db, err := s.options.Read(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.ListRuntimePresets(ctx)
	if err != nil {
		return nil, mapDBError(err)
	}
	presets := make([]Preset, 0, len(rows))
	for _, row := range rows {
		presets = append(presets, s.preset(row))
	}
	return presets, nil
}

func (s dbPresets) Get(ctx context.Context, key string) (Preset, error) {
	id, err := dbKey(key)
	if err != nil {
		return Preset{}, err
	}
	db, err := s.options.Read(ctx)
	if err != nil {
		return Preset{}, err
	}
	row, err := db.GetRuntimePreset(ctx, id)
	if err != nil {
		return Preset{}, mapDBError(err)
	}
	return s.preset(*row), nil
}

func (s dbPresets) Create(ctx context.Context, in PresetInput) (Preset, error) {
	db, err := s.write(ctx)
	if err != nil {
		return Preset{}, err
	}
	row, err := db.CreateRuntimePreset(ctx, database.RuntimePresetInput(in))
	if err != nil {
		return Preset{}, mapDBError(err)
	}
	return s.preset(*row), nil
}

func (s dbPresets) Update(ctx context.Context, key string, in PresetInput) (Preset, error) {
	id, err := dbKey(key)
	if err != nil {
		return Preset{}, err
	}
	db, err := s.write(ctx)
	if err != nil {
		return Preset{}, err
	}
	row, err := db.UpdateRuntimePreset(ctx, id, database.RuntimePresetInput(in))
	if err != nil {
		return Preset{}, mapDBError(err)
	}
	return s.preset(*row), nil
}

func (s dbPresets) Delete(ctx context.Context, key string) error {
	id, err := dbKey(key)
	if err != nil {
		return err
	}
	db, err := s.write(ctx)
	if err != nil {
		return err
	}
	return mapDBError(db.DeleteRuntimePreset(ctx, id))
}

type dbProfiles struct{ *dbSource }

func (s dbProfiles) List(ctx context.Context) ([]Profile, error) {
	db, err := s.options.Read(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := db.ListRuntimeProfiles(ctx)
	if err != nil {
		return nil, mapDBError(err)
	}
	profiles := make([]Profile, 0, len(rows))
	for _, row := range rows {
		profiles = append(profiles, s.profile(row))
	}
	return profiles, nil
}

func (s dbProfiles) Get(ctx context.Context, key string) (Profile, error) {
	id, err := dbKey(key)
	if err != nil {
		return Profile{}, err
	}
	db, err := s.options.Read(ctx)
	if err != nil {
		return Profile{}, err
	}
	row, err := db.GetRuntimeProfile(ctx, id)
	if err != nil {
		return Profile{}, mapDBError(err)
	}
	return s.profile(*row), nil
}

func (s dbProfiles) Create(ctx context.Context, in ProfileInput) (Profile, error) {
	db, err := s.write(ctx)
	if err != nil {
		return Profile{}, err
	}
	row, err := db.CreateRuntimeProfile(ctx, database.RuntimeProfileInput(in))
	if err != nil {
		return Profile{}, mapDBError(err)
	}
	return s.profile(*row), nil
}

func (s dbProfiles) Update(ctx context.Context, key string, in ProfileInput) (Profile, error) {
	id, err := dbKey(key)
	if err != nil {
		return Profile{}, err
	}
	db, err := s.write(ctx)
	if err != nil {
		return Profile{}, err
	}
	row, err := db.UpdateRuntimeProfile(ctx, id, database.RuntimeProfileInput(in))
	if err != nil {
		return Profile{}, mapDBError(err)
	}
	return s.profile(*row), nil
}

func (s dbProfiles) Delete(ctx context.Context, key string) error {
	id, err := dbKey(key)
	if err != nil {
		return err
	}
	db, err := s.write(ctx)
	if err != nil {
		return err
	}
	return mapDBError(db.DeleteRuntimeProfile(ctx, id))
}
