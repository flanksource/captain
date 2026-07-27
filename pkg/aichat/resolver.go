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
	Provider(context.Context, api.Config) (api.StreamingProvider, error)
}

type captainResolver struct{}

func (captainResolver) Models(_ context.Context) (ModelCatalogResponse, error) {
	configured := make([]string, 0, 4)
	for _, backend := range []api.Backend{
		api.BackendAnthropic, api.BackendOpenAI, api.BackendGemini, api.BackendDeepSeek,
	} {
		resolved, err := ai.ResolveAPIKey(backend)
		if err != nil {
			return nil, fmt.Errorf("resolve %s credentials: %w", backend, err)
		}
		if resolved.Token != "" {
			configured = append(configured, ai.BackendToProvider(backend))
		}
	}
	return ai.LiveCatalogInfo(configured)
}

func (captainResolver) Provider(_ context.Context, config api.Config) (api.StreamingProvider, error) {
	provider, err := ai.NewProvider(config)
	if err != nil {
		return nil, err
	}
	streaming, ok := api.ProviderAs[api.StreamingProvider](provider)
	if !ok {
		if closeErr := closeProvider(provider); closeErr != nil {
			return nil, fmt.Errorf("backend %q does not support streaming; close provider: %w", provider.GetBackend(), closeErr)
		}
		return nil, fmt.Errorf("backend %q does not support streaming", provider.GetBackend())
	}
	return streaming, nil
}

func closeProvider(provider api.Provider) error {
	if closer, ok := api.ProviderAs[api.CloseableProvider](provider); ok {
		return closer.Close()
	}
	return nil
}
