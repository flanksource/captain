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
	Sandbox     SandboxDefaults    `yaml:"sandbox,omitempty"`
	Credentials CredentialDefaults `yaml:"credentials,omitempty"`
}

// SandboxDefaults is the sandbox block of ~/.captain.yaml: a default selector
// plus a name→config map, shaped like AIDefaults' DefaultProvider + Providers.
type SandboxDefaults struct {
	// Default is the sandbox applied when neither the CLI flag nor the prompt
	// frontmatter selects one. Empty means off.
	Default string `yaml:"default,omitempty"`
	// Backends are the named sandbox configurations, keyed by the name written
	// in `sandbox:` frontmatter or passed to --sandbox.
	Backends map[string]SandboxBackend `yaml:"backends,omitempty"`
}

// IsZero lets yaml omit an empty sandbox block instead of writing `sandbox: {}`.
func (s SandboxDefaults) IsZero() bool {
	return s.Default == "" && len(s.Backends) == 0
}

// SandboxBackend is one named sandbox configuration. Kind selects the adapter;
// everything else is the adapter's own settings, carried verbatim for it to
// decode — this package deliberately knows nothing about what they mean.
type SandboxBackend struct {
	Kind    string         `yaml:"kind"`
	Options map[string]any `yaml:",inline"`
}

// SandboxSelection is a resolved sandbox choice: the adapter kind, the
// configured backend name it came from (empty when a bare kind was named), and
// that backend's settings.
type SandboxSelection struct {
	Kind    registry.SandboxKind
	Name    string
	Options map[string]any
}

// Resolve maps a selector — a configured backend name, or a bare adapter kind —
// to its selection. An empty selector falls back to Default, and an empty
// Default to "off". A selector that names neither a configured backend nor a
// kind is an error, as is a configured backend whose kind is unknown: silently
// running unsandboxed is the failure this method exists to prevent.
func (s SandboxDefaults) Resolve(selector string) (SandboxSelection, error) {
	name := strings.TrimSpace(selector)
	if name == "" {
		name = strings.TrimSpace(s.Default)
	}
	if backend, ok := s.Backends[name]; ok {
		// An empty kind must not fall through ParseSandboxKind, where empty
		// deliberately means "off": a backend whose kind is missing or
		// misspelled would silently disable confinement.
		if strings.TrimSpace(backend.Kind) == "" {
			return SandboxSelection{}, fmt.Errorf("sandbox backend %q declares no kind (valid: %s)",
				name, registry.SandboxKindList())
		}
		kind, valid := registry.ParseSandboxKind(backend.Kind)
		if !valid {
			return SandboxSelection{}, fmt.Errorf("sandbox backend %q has invalid kind %q (valid: %s)",
				name, backend.Kind, registry.SandboxKindList())
		}
		return SandboxSelection{Kind: kind, Name: name, Options: backend.Options}, nil
	}
	kind, ok := registry.ParseSandboxKind(name)
	if !ok {
		return SandboxSelection{}, fmt.Errorf("unknown sandbox %q: neither a configured backend nor an adapter kind (kinds: %s)",
			name, registry.SandboxKindList())
	}
	return SandboxSelection{Kind: kind}, nil
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
	Disabled        DisabledSelections          `yaml:"disabled,omitempty"`

	// File-wide generation settings. These are global on purpose: they are
	// properties of a run, not of a provider.
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
	// Mode is the runtime mechanism this provider defaults to: api|agent|cli|cmux.
	Mode            string `yaml:"mode,omitempty" json:"mode"`
	Model           string `yaml:"model,omitempty" json:"model"`
	ReasoningEffort string `yaml:"reasoningEffort,omitempty" json:"effort"`
}

// DisabledRuntime is one provider×mode pair switched off. It is a pair rather
// than a name because "cmux off for anthropic but not for openai" is exactly
// what neither the modes nor the providers list can say.
type DisabledRuntime struct {
	Provider string `yaml:"provider" json:"provider"`
	Mode     string `yaml:"mode" json:"mode"`
}

// DisabledSelections is the opt-out set edited from the whoami page: runtime
// modes, provider families, individual runtimes, models and effort tiers the
// user never wants captain to reach for. Tokens are matched case-insensitively.
//
// Models are keyed by provider rather than by webapp menu id —
// "google/gemini-3.5-flash", not "googleai/gemini-3.5-flash". A bare id with no
// slash disables that model everywhere.
type DisabledSelections struct {
	Modes     []string          `yaml:"modes,omitempty" json:"modes"`
	Providers []string          `yaml:"providers,omitempty" json:"providers"`
	Runtimes  []DisabledRuntime `yaml:"runtimes,omitempty" json:"runtimes"`
	Models    []string          `yaml:"models,omitempty" json:"models"`
	Efforts   []string          `yaml:"efforts,omitempty" json:"efforts"`
}

// Set converts the config lists into the registry lookup type.
func (d DisabledSelections) Set() registry.DisabledSet {
	runtimes := make([]registry.Runtime, 0, len(d.Runtimes))
	for _, r := range d.Runtimes {
		runtimes = append(runtimes, registry.Runtime{
			Provider: r.Provider,
			Mode:     registry.RuntimeMode(r.Mode),
		})
	}
	return registry.NewDisabledSet(d.Modes, d.Providers, runtimes, d.Models, d.Efforts)
}

// ApplyToRegistry installs the opt-out set process-wide so the resolution paths
// in pkg/api/registry honour it. Callers invoke it explicitly after Load and
// after every Update — loading the file is deliberately side-effect free.
func (c Config) ApplyToRegistry() {
	registry.SetDisabled(c.AI.Disabled.Set())
}

func (a AIDefaults) ActiveProvider() string {
	disabled := a.Disabled.Set()
	if p, ok := registry.ProviderByName(strings.TrimSpace(a.DefaultProvider)); ok && !disabled.Provider(p) {
		return p.Name
	}
	if !disabled.Provider(registry.Anthropic) {
		return registry.Anthropic.Name
	}
	for _, p := range registry.Providers() {
		if _, serves := p.Caps(registry.ModeAPI); serves && !disabled.Provider(p) {
			return p.Name
		}
	}
	return registry.Anthropic.Name
}

// Provider returns one provider's saved defaults. A provider with nothing saved
// returns the zero value, which the resolution path fills from the registry.
func (a AIDefaults) Provider(provider string) ProviderDefaults {
	return a.Providers[strings.TrimSpace(provider)]
}

type PromptDefaults struct {
	Dirs         []string             `yaml:"dirs,omitempty"`
	SchemaRepair SchemaRepairDefaults `yaml:"schemaRepair,omitempty"`
}

type SchemaRepairDefaults struct {
	Model string `yaml:"model,omitempty"`
	// Mode names the runtime mechanism (api|agent|cli|cmux). The provider follows
	// from the model name.
	Mode   string `yaml:"mode,omitempty"`
	Prompt string `yaml:"prompt,omitempty"`
}

// pathOverride redirects Path() away from $HOME. Empty string means
// "use os.UserHomeDir".
var pathOverride string

// SetPath redirects Path() to an explicit config file. It exists for
// processes that cannot rely on $HOME: a git receive hook runs as a child of
// whoever pushed, so its ambient home is the pusher's, not the one the
// receiver was configured under.
//
// Call SetPath during process startup, before concurrent calls to Path. The
// override is process-global and intentionally unsynchronized.
func SetPath(p string) { pathOverride = p }

// SetPathForTesting redirects Path() to the given absolute file path. Tests
// must call it with t.Cleanup(func() { SetPathForTesting("") }).
func SetPathForTesting(p string) { SetPath(p) }

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
	// A file written before runtimes became (model, mode) is rejected, not
	// upgraded. Rewriting it would mean guessing which runtime each composite
	// adapter id meant and then silently running on that guess; the user is the
	// only one who can say, and the message tells them exactly what to write.
	if err := checkRemovedKeys(path, data); err != nil {
		return Config{}, false, err
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

// normalize is the last step before a write. Every field the file carries is now
// one the reader accepts, so there is nothing to fold or clear — it exists as the
// single place a future write-time invariant belongs.
func normalize(cfg Config) Config {
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
