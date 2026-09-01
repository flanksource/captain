package aichat

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

func annotateProfileModels(resolved api.ResolvedSpec, models ModelCatalogResponse) {
	if len(resolved.Constraints.Models) == 0 {
		return
	}
	for index := range models {
		if resolved.AllowsModel(models[index].Runtime) {
			continue
		}
		layer := modelRestrictionLayer(resolved, models[index].Runtime)
		models[index].Configured = false
		models[index].Default = false
		models[index].Availability = restrictedAvailability(layer, resolved.Constraints.Models)
	}
}

func annotateProfileRuntimes(resolved api.ResolvedSpec, runtimes []api.RuntimeFamily) {
	if len(resolved.Constraints.Models) == 0 {
		return
	}
	for familyIndex := range runtimes {
		for modeIndex := range runtimes[familyIndex].Modes {
			mode := &runtimes[familyIndex].Modes[modeIndex]
			// Both sides name the same (provider, mode) pair: the family carries
			// provider identity and the entry carries the mode.
			provider, known := api.ProviderByName(runtimes[familyIndex].Provider)
			if !known {
				continue
			}
			if runtimeAllowed(resolved.Constraints.Models, provider, api.RuntimeMode(mode.Mode)) {
				continue
			}
			layer := runtimeRestrictionLayer(resolved, provider, api.RuntimeMode(mode.Mode))
			mode.Disabled = true
			if layer != nil {
				mode.DisabledReason = fmt.Sprintf("%s layer %s", layer.Scope, layer.Name)
			}
			mode.Availability = restrictedAvailability(layer, resolved.Constraints.Models)
		}
	}
}

func restrictedAvailability(layer *api.SpecLayer, allowed []string) api.Availability {
	reason := "Unavailable because the resolved runtime profile restricts the model catalog."
	if layer != nil {
		reason = fmt.Sprintf("Unavailable because %s layer %q restricts the model catalog.", layer.Scope, layer.Name)
	}
	return api.Availability{
		State:       api.AvailabilityDisabled,
		Reason:      reason,
		Remediation: "Select one of the allowed models: " + strings.Join(allowed, ", ") + ".",
	}
}

func modelRestrictionLayer(resolved api.ResolvedSpec, model api.Model) *api.SpecLayer {
	for index := len(resolved.Trace) - 1; index >= 0; index-- {
		layer := &resolved.Trace[index]
		if len(layer.Constraints.Models) > 0 && !(api.ResolvedSpec{Constraints: layer.Constraints}).AllowsModel(model) {
			return layer
		}
	}
	return nil
}

func runtimeRestrictionLayer(resolved api.ResolvedSpec, provider *api.ModelProvider, mode api.RuntimeMode) *api.SpecLayer {
	for index := len(resolved.Trace) - 1; index >= 0; index-- {
		layer := &resolved.Trace[index]
		if len(layer.Constraints.Models) > 0 && !runtimeAllowed(layer.Constraints.Models, provider, mode) {
			return layer
		}
	}
	return nil
}

// runtimeAllowed checks concrete registry rows so bare names follow the same
// matching rules as the model catalog instead of requiring a provider prefix.
func runtimeAllowed(models []string, provider *api.ModelProvider, mode api.RuntimeMode) bool {
	if provider == nil {
		return false
	}
	providerPrefix := provider.CatalogPrefix + "/"
	for _, selector := range models {
		if strings.HasPrefix(strings.TrimSpace(selector), providerPrefix) {
			return true
		}
	}
	resolved := api.ResolvedSpec{Constraints: api.RuntimeConstraints{Models: models}}
	for _, model := range provider.Models() {
		if !model.Preferred {
			continue
		}
		if known, available := provider.Availability(mode, model.ID); !known || !available {
			continue
		}
		candidate := api.Model{Name: model.ID, Provider: provider, Mode: mode}
		if mode == registry.ModeAPI {
			candidate.ID = provider.CatalogPrefix + "/" + model.ID
		}
		if resolved.AllowsModel(candidate) {
			return true
		}
	}
	return false
}

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
