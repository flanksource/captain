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
	// ConfiguredProviders returns the provider keys the caller holds credentials
	// for (anthropic | openai | google | deepseek). A credential belongs to a
	// provider, not to a runtime: every mode of a family authenticates the same
	// way, so there is no mode axis here.
	ConfiguredProviders(context.Context) ([]string, error)
	ProviderConfig(context.Context, ProviderConfigRequest) (api.Config, error)
}

func (s *Service) annotateConfiguredModels(ctx context.Context, models ModelCatalogResponse) error {
	configured, err := s.configuredProviders(ctx)
	if err != nil || configured == nil {
		return err
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
	configured, err := s.configuredProviders(ctx)
	if err != nil || configured == nil {
		return err
	}
	for familyIndex := range runtimes {
		if !configured[runtimes[familyIndex].Provider] {
			continue
		}
		for modeIndex := range runtimes[familyIndex].Modes {
			mode := &runtimes[familyIndex].Modes[modeIndex]
			if mode.Availability.State == api.AvailabilityMissingCredential {
				mode.Availability = api.Available()
			}
		}
	}
	return nil
}

// configuredProviders returns the credentialed provider keys as a set, or nil
// when no provider-config source is installed.
func (s *Service) configuredProviders(ctx context.Context) (map[string]bool, error) {
	if s.options.ProviderConfig == nil {
		return nil, nil
	}
	providers, err := s.options.ProviderConfig.ConfiguredProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("load configured chat providers: %w", err)
	}
	configured := make(map[string]bool, len(providers))
	for _, provider := range providers {
		if provider == "" {
			return nil, fmt.Errorf("configured chat provider key is required")
		}
		configured[provider] = true
	}
	return configured, nil
}

func (s *Service) prepareProviderConfig(ctx context.Context, config api.Config) (api.Config, error) {
	if s.options.ProviderConfig != nil {
		resolved, err := ai.Resolve(config.Model)
		if err != nil {
			return api.Config{}, fmt.Errorf("resolve chat model: %w", err)
		}
		config.Model = resolved
		config, err = s.options.ProviderConfig.ProviderConfig(ctx, ProviderConfigRequest{
			Model: resolved, Config: config,
		})
		if err != nil {
			return api.Config{}, fmt.Errorf("load chat provider config for %s: %w", api.RuntimeOf(resolved.Provider, resolved.Mode), err)
		}
		if !reflect.DeepEqual(config.Model, resolved) {
			return api.Config{}, fmt.Errorf("provider config source changed the resolved chat model from %q (%s) to %q (%s)",
				resolved.Name, api.RuntimeOf(resolved.Provider, resolved.Mode), config.Model.Name, api.RuntimeOf(config.Model.Provider, config.Model.Mode))
		}
	}
	return config, nil
}
