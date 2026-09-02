package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
	"github.com/flanksource/clicky/entity"
)

type runtimeCatalogContextKey struct{}

// ContextWithRuntimeCatalog pins the preset/profile catalog a request resolves
// against, so tests and embedding hosts can supply file-only sources.
func ContextWithRuntimeCatalog(ctx context.Context, catalog *runtimeprofiles.Catalog) context.Context {
	return context.WithValue(ctx, runtimeCatalogContextKey{}, catalog)
}

// buildRuntimeCatalog assembles the preset and profile sources: the monitored
// database, the user's ~/.config/captain directories, the directories named in
// ~/.captain.yaml, and the repository's .captain directories.
func buildRuntimeCatalog(ctx context.Context) (*runtimeprofiles.Catalog, error) {
	if catalog, ok := ctx.Value(runtimeCatalogContextKey{}).(*runtimeprofiles.Catalog); ok && catalog != nil {
		return catalog, nil
	}
	dbSource, err := runtimeprofiles.NewDBSource(runtimeprofiles.DBSourceOptions{
		Read: captainDB, Write: captainDefaultDB,
	})
	if err != nil {
		return nil, err
	}
	dirs, err := runtimeRecordDirs()
	if err != nil {
		return nil, err
	}
	sources := []runtimeprofiles.Source{dbSource}
	for _, dir := range dirs {
		source, err := runtimeprofiles.NewFileSource(runtimeprofiles.FileSourceOptions{
			Kind: dir.kind, Dir: dir.path, Label: dir.path, Implicit: dir.implicit,
		})
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return runtimeprofiles.NewCatalog(sources...)
}

type runtimeRecordDir struct {
	kind     runtimeprofiles.Kind
	path     string
	implicit bool
}

func runtimeRecordDirs() ([]runtimeRecordDir, error) {
	var dirs []runtimeRecordDir
	seen := map[string]bool{}
	add := func(kind runtimeprofiles.Kind, path string, implicit bool) {
		key := string(kind) + ":" + path
		if seen[key] {
			return
		}
		seen[key] = true
		dirs = append(dirs, runtimeRecordDir{kind: kind, path: path, implicit: implicit})
	}
	configHome, err := captainConfigHome()
	if err != nil {
		return nil, err
	}
	add(runtimeprofiles.KindPreset, filepath.Join(configHome, "presets"), true)
	add(runtimeprofiles.KindProfile, filepath.Join(configHome, "profiles"), true)
	if err := addConfiguredRuntimeDirs(add); err != nil {
		return nil, err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	add(runtimeprofiles.KindPreset, filepath.Join(cwd, ".captain", "presets"), true)
	add(runtimeprofiles.KindProfile, filepath.Join(cwd, ".captain", "profiles"), true)
	return dirs, nil
}

func addConfiguredRuntimeDirs(add func(runtimeprofiles.Kind, string, bool)) error {
	cfg, exists, err := captainconfig.Load()
	if err != nil || !exists {
		return err
	}
	configPath, err := captainconfig.Path()
	if err != nil {
		return err
	}
	base := filepath.Dir(configPath)
	for _, raw := range cfg.Runtime.PresetDirs {
		dir, err := resolvePromptDir(raw, base)
		if err != nil {
			return fmt.Errorf("runtime.presetDirs: %w", err)
		}
		add(runtimeprofiles.KindPreset, dir, false)
	}
	for _, raw := range cfg.Runtime.ProfileDirs {
		dir, err := resolvePromptDir(raw, base)
		if err != nil {
			return fmt.Errorf("runtime.profileDirs: %w", err)
		}
		add(runtimeprofiles.KindProfile, dir, false)
	}
	return nil
}

// captainConfigHome is $XDG_CONFIG_HOME/captain, defaulting to ~/.config/captain.
func captainConfigHome() (string, error) {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return filepath.Join(base, "captain"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "captain"), nil
}

// runtimeCatalogError maps catalog failures onto HTTP statuses for the entity
// surface; anything unrecognised stays an internal error.
func runtimeCatalogError(err error) error {
	var referenced runtimeprofiles.ReferencedError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &referenced):
		return entity.NewStatusErrorf(http.StatusConflict, "preset_in_use", "%v", err)
	case errors.Is(err, runtimeprofiles.ErrNotFound):
		return entity.NewStatusErrorf(http.StatusNotFound, "not_found", "%v", err)
	case errors.Is(err, runtimeprofiles.ErrAmbiguous):
		return entity.NewStatusErrorf(http.StatusConflict, "ambiguous", "%v", err)
	case errors.Is(err, runtimeprofiles.ErrNameTaken):
		return entity.NewStatusErrorf(http.StatusConflict, "name_taken", "%v", err)
	case errors.Is(err, runtimeprofiles.ErrReadOnly):
		return entity.NewStatusErrorf(http.StatusConflict, "read_only", "%v", err)
	case errors.Is(err, runtimeprofiles.ErrInvalid):
		return entity.NewStatusErrorf(http.StatusBadRequest, "invalid", "%v", err)
	default:
		return err
	}
}
