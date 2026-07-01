// Package captainconfig manages the user-level captain configuration file at
// ~/.captain.yaml. The file stores defaults applied by `captain ai *` commands
// when the corresponding flag is not passed. It is intentionally a thin
// data-only package: callers (the wizard, the AI commands) own the policy of
// when and how to overlay these defaults onto runtime options.
package captainconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Config struct {
	AI      AIDefaults     `yaml:"ai"`
	Prompts PromptDefaults `yaml:"prompts"`
}

type AIDefaults struct {
	Backend         string  `yaml:"backend,omitempty"`
	Model           string  `yaml:"model,omitempty"`
	ReasoningEffort string  `yaml:"reasoningEffort,omitempty"`
	BudgetUSD       float64 `yaml:"budgetUSD,omitempty"`
	MaxTokens       int     `yaml:"maxTokens,omitempty"`
	Temperature     float64 `yaml:"temperature,omitempty"`
	Timeout         string  `yaml:"timeout,omitempty"`
	NoCache         bool    `yaml:"noCache,omitempty"`
	NoMCP           bool    `yaml:"noMCP,omitempty"`
	NoHooks         bool    `yaml:"noHooks,omitempty"`
	NoSkills        bool    `yaml:"noSkills,omitempty"`
	NoUser          bool    `yaml:"noUser,omitempty"`
	NoProject       bool    `yaml:"noProject,omitempty"`
	NoMemory        bool    `yaml:"noMemory,omitempty"`
}

type PromptDefaults struct {
	Dirs []string `yaml:"dirs,omitempty"`
}

// pathOverride lets tests redirect Path() to a temp directory without touching
// $HOME. Empty string means "use os.UserHomeDir".
var pathOverride string

// SetPathForTesting redirects Path() to the given absolute file path. Tests
// must call it with t.Cleanup(func() { SetPathForTesting("") }).
func SetPathForTesting(p string) { pathOverride = p }

// Path returns the absolute path to the captain config file. Currently fixed
// at ~/.captain.yaml; the location is intentionally not configurable via env
// to keep the wizard's verification step (`cat ~/.captain.yaml`) authoritative.
func Path() (string, error) {
	if pathOverride != "" {
		return pathOverride, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".captain.yaml"), nil
}

// Load reads the config file. The bool return is false when the file does
// not exist (a normal first-run state), in which case err is nil and the
// returned Config is zero-valued.
func Load() (Config, bool, error) {
	path, err := Path()
	if err != nil {
		return Config{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, false, nil
		}
		return Config{}, false, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, false, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, true, nil
}

// Save writes the config atomically: yaml-marshal to a sibling temp file in
// the same directory, then rename. The temp+rename keeps a reader from ever
// observing a half-written file.
func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".captain-*.yaml")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	return nil
}
