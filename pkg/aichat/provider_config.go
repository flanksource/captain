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
		if configured[models[i].Provider] && models[i].Availability.State == api.AvailabilityMissingCredential {
			models[i].Configured = true
			models[i].Availability = api.Available()
		}
	}
	return nil
}

func (s *Service) annotateConfiguredRuntimes(ctx context.Context, runtimes []api.RuntimeFamily) error {
	if s.options.ProviderConfig == nil {
		return nil
	}
	backends, err := s.options.ProviderConfig.ConfiguredProviders(ctx)
	if err != nil {
		return fmt.Errorf("load configured chat providers: %w", err)
	}
	configured := make(map[api.Backend]bool, len(backends))
	for _, backend := range backends {
		if backend == "" {
			return fmt.Errorf("configured chat provider backend is required")
		}
		configured[backend] = true
	}
	for familyIndex := range runtimes {
		for modeIndex := range runtimes[familyIndex].Modes {
			mode := &runtimes[familyIndex].Modes[modeIndex]
			if configured[api.Backend(mode.Backend)] && mode.Availability.State == api.AvailabilityMissingCredential {
				mode.Availability = api.Available()
			}
		}
	}
	return nil
}

func (s *Service) prepareProviderConfig(ctx context.Context, config api.Config) (api.Config, error) {
	if s.options.ProviderConfig != nil {
		resolved, err := ai.ResolveModelSelectors(config.Model)
		if err != nil {
			return api.Config{}, fmt.Errorf("resolve chat model: %w", err)
		}
		config.Model = resolved
		config, err = s.options.ProviderConfig.ProviderConfig(ctx, ProviderConfigRequest{
			Model: resolved, Config: config,
		})
		if err != nil {
			return api.Config{}, fmt.Errorf("load chat provider config for %s: %w", resolved.Backend, err)
		}
		if !reflect.DeepEqual(config.Model, resolved) {
			return api.Config{}, fmt.Errorf("provider config source changed the resolved chat model from %q (%s) to %q (%s)",
				resolved.Name, resolved.Backend, config.Model.Name, config.Model.Backend)
		}
	}
	return config, nil
}
