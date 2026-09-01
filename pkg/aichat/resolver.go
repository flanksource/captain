package aichat

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	_ "github.com/flanksource/captain/pkg/ai/provider"
	"github.com/flanksource/captain/pkg/api"
)

// Resolver is the injectable boundary around Captain's model catalog and
// provider construction. The default implementation delegates to Captain's
// canonical resolver; tests may replace it with a fake provider.
type Resolver interface {
	Models(context.Context) (ModelCatalogResponse, error)
	Runtimes(context.Context) ([]api.RuntimeFamily, error)
	Provider(context.Context, api.Config) (api.StreamingProvider, error)
}

type captainResolver struct{}

func (captainResolver) Models(_ context.Context) (ModelCatalogResponse, error) {
	// A credential belongs to a provider, not to a runtime: every mode of a
	// family authenticates the same way, so only the API mode is probed.
	providers := api.Providers()
	configured := make([]string, 0, len(providers))
	for _, p := range providers {
		resolved, err := ai.ResolveAPIKey(p, api.ModeAPI)
		if err != nil {
			return nil, fmt.Errorf("resolve %s credentials: %w", p.Name, err)
		}
		if resolved.Token != "" {
			configured = append(configured, p.Name)
		}
	}
	return ai.LiveCatalogInfo(configured)
}

func (captainResolver) Runtimes(_ context.Context) ([]api.RuntimeFamily, error) {
	return ai.LiveRuntimeCatalog()
}

func (captainResolver) Provider(_ context.Context, config api.Config) (api.StreamingProvider, error) {
	provider, err := ai.NewProvider(config)
	if err != nil {
		return nil, err
	}
	streaming, ok := api.ProviderAs[api.StreamingProvider](provider)
	if !ok {
		if closeErr := closeProvider(provider); closeErr != nil {
			return nil, fmt.Errorf("runtime %s does not support streaming; close provider: %w", provider.GetRuntime(), closeErr)
		}
		return nil, fmt.Errorf("runtime %s does not support streaming", provider.GetRuntime())
	}
	return streaming, nil
}

func closeProvider(provider api.Provider) error {
	if closer, ok := api.ProviderAs[api.CloseableProvider](provider); ok {
		return closer.Close()
	}
	return nil
}
