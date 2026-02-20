package ai

import (
	"fmt"
	"os"
)

type ProviderFactory func(cfg Config) Provider

var factories = map[Backend]ProviderFactory{}

func RegisterProvider(backend Backend, factory ProviderFactory) {
	factories[backend] = factory
}

func NewProvider(cfg Config) (Provider, error) {
	if cfg.Model == "" {
		return nil, fmt.Errorf("model cannot be empty")
	}

	backend := cfg.Backend
	if backend == "" {
		var err error
		backend, err = InferBackend(cfg.Model)
		if err != nil {
			return nil, err
		}
	}
	cfg.Backend = backend

	factory, ok := factories[backend]
	if !ok {
		return nil, fmt.Errorf("no provider registered for backend: %s", backend)
	}

	if cfg.APIKey == "" {
		cfg.APIKey = GetAPIKeyFromEnv(backend)
	}

	return factory(cfg), nil
}

func GetAPIKeyFromEnv(backend Backend) string {
	envVars := map[Backend][]string{
		BackendAnthropic: {"ANTHROPIC_API_KEY"},
		BackendGemini:    {"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		BackendGeminiCLI: {},
		BackendClaudeCLI: {},
		BackendCodexCLI:  {},
	}
	for _, envVar := range envVars[backend] {
		if key := os.Getenv(envVar); key != "" {
			return key
		}
	}
	return ""
}
