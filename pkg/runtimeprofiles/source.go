package runtimeprofiles

import "context"

// Store is the per-kind persistence contract a Source exposes. Keys are
// source-local (a database uuid, a file name stem); the catalog wraps them in
// encoded ids. Get, Update and Delete report a missing key as ErrNotFound, and
// Create reports a key already in use as ErrNameTaken.
type Store[R any, I any] interface {
	List(ctx context.Context) ([]R, error)
	Get(ctx context.Context, key string) (R, error)
	Create(ctx context.Context, input I) (R, error)
	Update(ctx context.Context, key string, input I) (R, error)
	Delete(ctx context.Context, key string) error
}

// Source is one backing store of runtime records. Presets and Profiles return
// nil for a kind that Info().Records does not list.
type Source interface {
	Info() SourceInfo
	Presets() Store[Preset, PresetInput]
	Profiles() Store[Profile, ProfileInput]
}
