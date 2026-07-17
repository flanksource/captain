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
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
	"golang.org/x/sys/unix"
	"gopkg.in/yaml.v3"
)

type Config struct {
	AI          AIDefaults         `yaml:"ai"`
	Prompts     PromptDefaults     `yaml:"prompts"`
	Attachments AttachmentDefaults `yaml:"attachments"`
}

type AttachmentDefaults struct {
	Directory       string `yaml:"directory,omitempty"`
	MaxFileBytes    int64  `yaml:"maxFileBytes,omitempty"`
	MaxRequestBytes int64  `yaml:"maxRequestBytes,omitempty"`
	MaxFiles        int    `yaml:"maxFiles,omitempty"`
	Retention       string `yaml:"retention,omitempty"`
}

func (a AttachmentDefaults) WithDefaults() AttachmentDefaults {
	if a.Directory == "" {
		a.Directory = ".captain/attachments"
	}
	if a.MaxFileBytes == 0 {
		a.MaxFileBytes = 20 << 20
	}
	if a.MaxRequestBytes == 0 {
		a.MaxRequestBytes = 50 << 20
	}
	if a.MaxFiles == 0 {
		a.MaxFiles = 10
	}
	if a.Retention == "" {
		a.Retention = "30d"
	}
	return a
}

type AIDefaults struct {
	DefaultProvider string                      `yaml:"defaultProvider,omitempty"`
	Providers       map[string]ProviderDefaults `yaml:"providers,omitempty"`

	// Legacy global selection fields are read so existing configurations can be
	// projected into the provider map. Save omits them after migration.
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

type ProviderDefaults struct {
	Agent           string `yaml:"agent,omitempty" json:"agent"`
	Model           string `yaml:"model,omitempty" json:"model"`
	ReasoningEffort string `yaml:"reasoningEffort,omitempty" json:"effort"`
}

func (a AIDefaults) ActiveProvider() string {
	if provider := registry.Backend(strings.TrimSpace(a.DefaultProvider)); provider != "" && provider.Provider() == provider {
		return string(provider)
	}
	if provider := registry.Backend(strings.TrimSpace(a.Backend)).Provider(); provider != "" {
		return string(provider)
	}
	if backend, err := registry.InferBackend(strings.TrimSpace(a.Model)); err == nil {
		return string(backend.Provider())
	}
	return string(registry.AnthropicProvider)
}

func (a AIDefaults) legacyProvider() string {
	if provider := registry.Backend(strings.TrimSpace(a.Backend)).Provider(); provider != "" {
		return string(provider)
	}
	if backend, err := registry.InferBackend(strings.TrimSpace(a.Model)); err == nil {
		return string(backend.Provider())
	}
	return strings.TrimSpace(a.DefaultProvider)
}

func (a AIDefaults) Provider(provider string) ProviderDefaults {
	provider = strings.TrimSpace(provider)
	defaults := a.Providers[provider]
	if provider != a.legacyProvider() {
		return defaults
	}
	legacyAgent := registry.Backend(strings.TrimSpace(a.Backend))
	if defaults.Agent == "" && legacyAgent.Provider() == registry.Backend(provider) {
		defaults.Agent = string(legacyAgent)
	}
	if defaults.Model == "" {
		defaults.Model = strings.TrimSpace(a.Model)
	}
	if defaults.ReasoningEffort == "" {
		defaults.ReasoningEffort = strings.TrimSpace(a.ReasoningEffort)
	}
	return defaults
}

type PromptDefaults struct {
	Dirs         []string             `yaml:"dirs,omitempty"`
	SchemaRepair SchemaRepairDefaults `yaml:"schemaRepair,omitempty"`
}

type SchemaRepairDefaults struct {
	Model   string `yaml:"model,omitempty"`
	Backend string `yaml:"backend,omitempty"`
	Prompt  string `yaml:"prompt,omitempty"`
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
	return load(path)
}

func load(path string) (Config, bool, error) {
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
	return withLock(path, func() error { return write(path, normalize(cfg)) })
}

// Update serializes a read-modify-write operation so concurrent CLI and web
// configuration changes cannot overwrite unrelated settings.
func Update(update func(*Config) error) error {
	if update == nil {
		return fmt.Errorf("config update function is required")
	}
	path, err := Path()
	if err != nil {
		return err
	}
	return withLock(path, func() error {
		cfg, _, err := load(path)
		if err != nil {
			return err
		}
		if err := update(&cfg); err != nil {
			return err
		}
		return write(path, normalize(cfg))
	})
}

func normalize(cfg Config) Config {
	a := &cfg.AI
	hasLegacy := a.Backend != "" || a.Model != "" || a.ReasoningEffort != ""
	if hasLegacy {
		provider := a.legacyProvider()
		if provider == "" {
			provider = a.ActiveProvider()
		}
		if a.Providers == nil {
			a.Providers = map[string]ProviderDefaults{}
		}
		a.Providers[provider] = a.Provider(provider)
		if a.DefaultProvider == "" {
			a.DefaultProvider = provider
		}
	}
	a.Backend, a.Model, a.ReasoningEffort = "", "", ""
	return cfg
}

func withLock(path string, action func() error) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("ensure %s: %w", dir, err)
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("open config lock: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return fmt.Errorf("lock config: %w", err)
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	return action()
}

func write(path string, cfg Config) error {
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
