package aichat

import (
	"context"
	"fmt"
	"reflect"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// ProviderConfigRequest carries the canonically resolved model and the runtime
// config assembled by the chat service.
type ProviderConfigRequest struct {
	Model  api.Model
	Config api.Config
}

// ProviderConfigSource supplies request-scoped provider identities and
// credentials without owning provider resolution or construction.
type ProviderConfigSource interface {
	ConfiguredProviders(context.Context) ([]api.Backend, error)
	ProviderConfig(context.Context, ProviderConfigRequest) (api.Config, error)
}

func (s *Service) annotateConfiguredModels(ctx context.Context, models ModelCatalogResponse) error {
	if s.options.ProviderConfig == nil {
		return nil
	}
	backends, err := s.options.ProviderConfig.ConfiguredProviders(ctx)
	if err != nil {
		return fmt.Errorf("load configured chat providers: %w", err)
	}
	configured := make(map[string]bool, len(backends))
	for _, backend := range backends {
		if backend == "" {
			return fmt.Errorf("configured chat provider backend is required")
		}
		configured[ai.BackendToProvider(backend)] = true
	}
	for i := range models {
		models[i].Configured = models[i].Configured || configured[models[i].Provider]
	}
	return nil
}

func (s *Service) resolveProvider(ctx context.Context, config api.Config) (api.StreamingProvider, error) {
	if s.options.ProviderConfig != nil {
		resolved, err := ai.ResolveModelSelectors(config.Model)
		if err != nil {
			return nil, fmt.Errorf("resolve chat model: %w", err)
		}
		config.Model = resolved
		config, err = s.options.ProviderConfig.ProviderConfig(ctx, ProviderConfigRequest{
			Model: resolved, Config: config,
		})
		if err != nil {
			return nil, fmt.Errorf("load chat provider config for %s: %w", resolved.Backend, err)
		}
		if !reflect.DeepEqual(config.Model, resolved) {
			return nil, fmt.Errorf("provider config source changed the resolved chat model from %q (%s) to %q (%s)",
				resolved.Name, resolved.Backend, config.Model.Name, config.Model.Backend)
		}
	}
	return s.resolver.Provider(ctx, config)
}
