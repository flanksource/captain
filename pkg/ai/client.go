package ai

import (
	"fmt"
	"os"
)

type ProviderFactory func(cfg Config) (Provider, error)

var factories = map[Backend]ProviderFactory{}

func RegisterProvider(backend Backend, factory ProviderFactory) {
	factories[backend] = factory
}

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

func GetAPIKeyFromEnv(backend Backend) string {
	for _, envVar := range AuthEnvVars(backend) {
		if key := os.Getenv(envVar); key != "" {
			return key
		}
	}
	return ""
}
