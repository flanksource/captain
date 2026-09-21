package budgets

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/database"
)

// DefaultCatalogOptions supplies the database and loaded Captain configuration.
type DefaultCatalogOptions struct {
	Read   func(context.Context) (*database.DB, error)
	Write  func(context.Context) (*database.DB, error)
	Config *captainconfig.Config
}

// NewDefaultCatalog combines the database with one-rule-per-file YAML sources
// under ~/.config/captain/budgets and runtime.budgetDirs.
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
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		home = xdg
	} else {
		home = filepath.Join(home, ".config")
	}
	defaultDir := filepath.Join(home, "captain", "budgets")
	defaultSource, err := NewFileSource(FileSourceOptions{Dir: defaultDir, Label: defaultDir, Implicit: true})
	if err != nil {
		return nil, err
	}
	sources = append(sources, defaultSource)
	cfg := options.Config
	if cfg == nil {
		loaded, _, err := captainconfig.Load()
		if err != nil {
			return nil, err
		}
		cfg = &loaded
	}
	configPath, err := captainconfig.Path()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{defaultDir: true}
	for _, raw := range cfg.Runtime.BudgetDirs {
		dir, err := resolveBudgetDir(raw, filepath.Dir(configPath))
		if err != nil {
			return nil, fmt.Errorf("runtime.budgetDirs: %w", err)
		}
		if seen[dir] {
			continue
		}
		seen[dir] = true
		source, err := NewFileSource(FileSourceOptions{Dir: dir, Label: dir})
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return NewCatalog(sources...)
}

func resolveBudgetDir(raw, base string) (string, error) {
	dir := strings.TrimSpace(raw)
	if dir == "" {
		return "", fmt.Errorf("budget dir cannot be empty")
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~/"))
	} else if strings.HasPrefix(dir, "~") {
		return "", fmt.Errorf("unsupported home-relative budget dir %q", raw)
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
		return "", fmt.Errorf("budget dir %s: %w", abs, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("budget dir %s is not a directory", abs)
	}
	return filepath.EvalSymlinks(abs)
}
