package ai

import (
	"github.com/flanksource/captain/pkg/api"
)

// The provider registry now lives in pkg/api (the stable runtime contract).
// ProviderFactory is re-exported as an alias; the registration/construction
// entrypoints are thin wrappers so existing call sites (and the blank-import
// self-registration in pkg/ai/provider) keep funneling into the single api
// registry unchanged.
type ProviderFactory = api.ProviderFactory

// RegisterProvider registers a factory for a backend in the shared api registry.
func RegisterProvider(backend Backend, factory ProviderFactory) {
	api.RegisterProvider(backend, factory)
}

// NewProvider constructs the registered provider for cfg's backend.
func NewProvider(cfg Config) (Provider, error) {
	return api.NewProvider(cfg)
}

// GetAPIKeyFromEnv returns the first non-empty value among a backend's auth env vars.
func GetAPIKeyFromEnv(backend Backend) string {
	return api.GetAPIKeyFromEnv(backend)
}
