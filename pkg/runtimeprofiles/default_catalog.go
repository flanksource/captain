package runtimeprofiles

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/database"
)

// DefaultCatalogOptions supplies a host's database, working directory and config.
type DefaultCatalogOptions struct {
	// Read and Write are lazy database openers. Omit both for file-only catalogs;
	// omitting only Write keeps the database read-only.
	Read  func(context.Context) (*database.DB, error)
	Write func(context.Context) (*database.DB, error)
	// Cwd is the absolute repository directory; empty uses the process directory.
	Cwd string
	// Config avoids reloading ~/.captain.yaml when the host already loaded it.
	// Relative runtime directories still resolve against captainconfig.Path().
	Config *captainconfig.Config
}

// NewDefaultCatalog discovers the database, user, configured and repo sources.
// Database openers run only when records are read or written.
func NewDefaultCatalog(ctx context.Context, options DefaultCatalogOptions) (*Catalog, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var sources []Source
	if options.Read != nil || options.Write != nil {
		source, err := NewDBSource(DBSourceOptions{Read: options.Read, Write: options.Write})
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	dirs, err := runtimeRecordDirs(options)
	if err != nil {
		return nil, err
	}
	for _, dir := range dirs {
		source, err := NewFileSource(dir)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return NewCatalog(sources...)
}

func runtimeRecordDirs(options DefaultCatalogOptions) ([]FileSourceOptions, error) {
	var dirs []FileSourceOptions
	seen := map[string]bool{}
	add := func(kind Kind, path string, implicit bool) {
		key := string(kind) + ":" + path
		if seen[key] {
			return
		}
		seen[key] = true
		dirs = append(dirs, FileSourceOptions{Kind: kind, Dir: path, Label: path, Implicit: implicit})
	}
	configHome, err := captainConfigHome()
	if err != nil {
		return nil, err
	}
	add(KindPreset, filepath.Join(configHome, "presets"), true)
	add(KindProfile, filepath.Join(configHome, "profiles"), true)
	if err := addConfiguredRuntimeDirs(options.Config, add); err != nil {
		return nil, err
	}
	cwd := options.Cwd
	if cwd == "" {
		cwd, err = os.Getwd()
		if err != nil {
			return nil, err
		}
	}
	add(KindPreset, filepath.Join(cwd, ".captain", "presets"), true)
	add(KindProfile, filepath.Join(cwd, ".captain", "profiles"), true)
	return dirs, nil
}

func addConfiguredRuntimeDirs(cfg *captainconfig.Config, add func(Kind, string, bool)) error {
	if cfg == nil {
		loaded, _, err := captainconfig.Load()
		if err != nil {
			return err
		}
		cfg = &loaded
	}
	if cfg.Runtime.IsZero() {
		return nil
	}
	configPath, err := captainconfig.Path()
	if err != nil {
		return err
	}
	for _, entry := range []struct {
		kind Kind
		dirs []string
	}{
		{KindPreset, cfg.Runtime.PresetDirs},
		{KindProfile, cfg.Runtime.ProfileDirs},
	} {
		for _, raw := range entry.dirs {
			dir, err := resolveRuntimeDir(raw, filepath.Dir(configPath))
			if err != nil {
				return fmt.Errorf("runtime.%sDirs: %w", entry.kind, err)
			}
			add(entry.kind, dir, false)
		}
	}
	return nil
}

func resolveRuntimeDir(raw, base string) (string, error) {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return "", fmt.Errorf("runtime dir cannot be empty")
	}
	if strings.HasPrefix(dir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		switch {
		case dir == "~":
			dir = home
		case strings.HasPrefix(dir, "~/"):
			dir = filepath.Join(home, dir[2:])
		default:
			return "", fmt.Errorf("unsupported home-relative runtime dir %q", raw)
		}
	}
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(base, dir)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("runtime dir %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("runtime dir %s is not a directory", abs)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve runtime dir %s: %w", abs, err)
	}
	return filepath.Clean(resolved), nil
}

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
