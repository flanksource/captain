package api

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/credentials"
)

type ResolvedAPIKey struct {
	Token  string
	Source string
	Detail string
}

// ProviderFactory constructs a Provider for a runtime from a Config.
type ProviderFactory func(cfg Config) (Provider, error)

// requireRuntimeBinary refuses a runtime whose executable is not installed,
// before any work is attempted.
//
// The local modes are the default for the families that serve them, so this is
// the first thing a user without the tooling hits. It must say which binary is
// missing and how to get running anyway: the failure it replaces surfaced from
// deep inside an adapter as an exec error naming "tsx", which explains nothing
// to someone who asked for claude-opus-5. It never falls back to another mode —
// quietly running somewhere other than where the user pointed is the whole class
// of bug this refactor exists to end.
func requireRuntimeBinary(p *registry.Provider, mode registry.RuntimeMode) error {
	caps, ok := p.Caps(mode)
	if !ok || caps.RequiredBinary == "" {
		return nil
	}
	if _, err := exec.LookPath(caps.RequiredBinary); err == nil {
		return nil
	}
	return fmt.Errorf(
		"%s needs %q on PATH, which is not installed; install it, pass --mode api to use %s's API instead, or run `captain whoami` to see every runtime's status",
		registry.RuntimeOf(p, mode), caps.RequiredBinary, p.Name)
}

// factories is the process-global adapter registry, keyed by provider×mode. It
// is unexported on purpose: it is internal plumbing, mutated only through
// RegisterProvider and read only through NewProvider, so the public api surface
// never exposes the map itself.
var factories = map[Runtime]ProviderFactory{}

// RegisterProvider registers a factory for a runtime. Provider packages call it
// from init(); the registration is global and read by NewProvider.
func RegisterProvider(runtime Runtime, factory ProviderFactory) {
	factories[runtime] = factory
}

// NewProvider constructs the registered adapter for cfg's runtime, filling the
// API key from the environment when unset.
//
// This is the resolution boundary: cfg.Model is resolved here, once, into the
// exact model id the adapter will send plus the mode and provider that serve it.
// Adapters therefore receive a model they can use verbatim — they used to
// re-normalize the id themselves, nine different times, each swallowing the
// failure. Resolving an already-resolved model is a no-op.
func NewProvider(cfg Config) (Provider, error) {
	if cfg.Model.Name == "" {
		return nil, fmt.Errorf("model cannot be empty; pass --model or run `captain configure` to set a default")
	}

	resolved, err := registry.ResolveModel(cfg.Model)
	if err != nil {
		return nil, err
	}
	cfg.Model = resolved
	provider, mode, err := cfg.Model.Runtime()
	if err != nil {
		return nil, err
	}
	if err := requireRuntimeBinary(provider, mode); err != nil {
		return nil, err
	}

	// The legacy boolean only speaks when no explicit selection was made:
	// ResolvedSandbox gives SandboxSelection precedence, so a caller selecting
	// e.g. "none" alongside a stale Sandbox=true must not be rejected as srt.
	// An unsupported sandbox × mode pairing is a validation error, not a
	// silent no-op: the adapter would have no seam to act on, and running
	// unsandboxed anyway is the failure this check exists to prevent.
	if cfg.SandboxSelection != nil {
		descriptor, ok := registry.SandboxFor(cfg.SandboxSelection.Kind)
		if !ok {
			return nil, fmt.Errorf("unknown sandbox kind %q; want one of: %s", cfg.SandboxSelection.Kind, registry.SandboxKindList())
		}
		if err := descriptor.ValidateMode(mode); err != nil {
			return nil, err
		}
	}

	runtime := RuntimeOf(provider, mode)
	factory, ok := factories[runtime]
	if !ok {
		return nil, fmt.Errorf("no adapter registered for %s", runtime)
	}

	if cfg.APIKey == "" {
		resolved, err := ResolveAPIKey(provider, mode)
		if err != nil {
			return nil, err
		}
		cfg.APIKey = resolved.Token
	}

	return factory(cfg)
}

// ResolveAPIKey resolves a direct provider credential from Captain's vault,
// then from the provider's supported environment variables. The vault is keyed
// by provider: a local transport rides the CLI's own login and only consults the
// environment.
func ResolveAPIKey(p *ModelProvider, mode RuntimeMode) (ResolvedAPIKey, error) {
	if mode.Kind() != "api" {
		for _, envVar := range AuthEnvVars(p, mode) {
			if token := os.Getenv(envVar); token != "" {
				return ResolvedAPIKey{Token: token, Source: credentials.SourceEnvironment, Detail: envVar}, nil
			}
		}
		return ResolvedAPIKey{}, nil
	}
	vault, err := credentials.DefaultVault()
	if err != nil {
		return ResolvedAPIKey{}, err
	}
	resolved, err := vault.Resolve(p.Name, AuthEnvVars(p, mode), os.Getenv)
	if err != nil {
		return ResolvedAPIKey{}, err
	}
	return ResolvedAPIKey(resolved), nil
}

// GetAPIKeyFromEnv returns the first non-empty value among a runtime's auth
// environment variables.
func GetAPIKeyFromEnv(p *ModelProvider, mode RuntimeMode) string {
	for _, envVar := range AuthEnvVars(p, mode) {
		if key := os.Getenv(envVar); key != "" {
			return key
		}
	}
	return ""
}
