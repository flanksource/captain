package api

import (
	"fmt"
	"os"
)

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

	factory, ok := factories[backend]
	if !ok {
		return nil, fmt.Errorf("no provider registered for backend: %s", backend)
	}

	if cfg.APIKey == "" {
		cfg.APIKey = GetAPIKeyFromEnv(backend)
	}

	return factory(cfg)
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
