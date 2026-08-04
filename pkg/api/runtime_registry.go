package api

import (
	"fmt"
	"os"

	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/credentials"
)

type ResolvedAPIKey struct {
	Token  string
	Source string
	Detail string
}

// ProviderFactory constructs a Provider for a backend from a Config.
type ProviderFactory func(cfg Config) (Provider, error)

// factories is the process-global backend registry. It is unexported on purpose:
// it is internal plumbing, mutated only through RegisterProvider and read only
// through NewProvider, so the public api surface never exposes the map itself.
var factories = map[Backend]ProviderFactory{}

// RegisterProvider registers a factory for a backend. Provider packages call it
// from init(); the registration is global and read by NewProvider.
func RegisterProvider(backend Backend, factory ProviderFactory) {
	factories[backend] = factory
}

// NewProvider constructs the registered provider for cfg's backend, inferring the
// backend from the model name and filling the API key from the environment when
// unset.
func NewProvider(cfg Config) (Provider, error) {
	backend := cfg.Model.Backend

	if cfg.Model.Name == "" {
		return nil, fmt.Errorf("model cannot be empty; pass --model or run `captain configure` to set a default")
	}

	if backend == "" {
		var err error
		backend, err = InferBackend(cfg.Model.Name)
		if err != nil {
			return nil, err
		}
	}
	cfg.Model.Backend = backend
	if cfg.Sandbox && backend.Mode() != registry.ModeCLI {
		return nil, fmt.Errorf("sandbox-runtime requires a CLI backend, got %s", backend)
	}
	// An unsupported sandbox × backend pairing is a validation error, not a
	// silent no-op: the adapter would have no seam to act on, and running
	// unsandboxed anyway is the failure this check exists to prevent.
	if cfg.SandboxSelection != nil {
		descriptor, ok := registry.SandboxFor(cfg.SandboxSelection.Kind)
		if !ok {
			return nil, fmt.Errorf("unknown sandbox kind %q; want one of: %s", cfg.SandboxSelection.Kind, registry.SandboxKindList())
		}
		if err := descriptor.ValidateMode(backend.Mode()); err != nil {
			return nil, err
		}
	}

	factory, ok := factories[backend]
	if !ok {
		return nil, fmt.Errorf("no provider registered for backend: %s", backend)
	}

	if cfg.APIKey == "" {
		resolved, err := ResolveAPIKey(backend)
		if err != nil {
			return nil, err
		}
		cfg.APIKey = resolved.Token
	}

	return factory(cfg)
}

// ResolveAPIKey resolves a direct provider credential from Captain's vault,
// then from the provider's supported environment variables.
func ResolveAPIKey(backend Backend) (ResolvedAPIKey, error) {
	if backend.Kind() != "api" {
		for _, envVar := range AuthEnvVars(backend) {
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
	resolved, err := vault.Resolve(string(backend), AuthEnvVars(backend), os.Getenv)
	if err != nil {
		return ResolvedAPIKey{}, err
	}
	return ResolvedAPIKey(resolved), nil
}

// GetAPIKeyFromEnv returns the first non-empty value among a backend's auth
// environment variables.
func GetAPIKeyFromEnv(backend Backend) string {
	for _, envVar := range AuthEnvVars(backend) {
		if key := os.Getenv(envVar); key != "" {
			return key
		}
	}
	return ""
}
