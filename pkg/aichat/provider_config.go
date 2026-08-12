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
			backend := api.Backend(mode.Backend)
			if runtimeAllowed(resolved.Constraints.Models, backend) {
				continue
			}
			layer := runtimeRestrictionLayer(resolved, backend)
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

func runtimeRestrictionLayer(resolved api.ResolvedSpec, backend api.Backend) *api.SpecLayer {
	for index := len(resolved.Trace) - 1; index >= 0; index-- {
		layer := &resolved.Trace[index]
		if len(layer.Constraints.Models) > 0 && !runtimeAllowed(layer.Constraints.Models, backend) {
			return layer
		}
	}
	return nil
}

// runtimeAllowed checks concrete registry rows so bare names follow the same
// matching rules as the model catalog instead of requiring a provider prefix.
func runtimeAllowed(models []string, backend api.Backend) bool {
	providerPrefix := ai.BackendToProvider(backend) + "/"
	for _, selector := range models {
		if strings.HasPrefix(strings.TrimSpace(selector), providerPrefix) {
			return true
		}
	}

	provider, mode, ok := registry.ProviderFor(backend)
	if !ok {
		return false
	}
	resolved := api.ResolvedSpec{Constraints: api.RuntimeConstraints{Models: models}}
	for _, model := range provider.Models() {
		if !model.Preferred {
			continue
		}
		if known, available := provider.Availability(mode, model.ID); !known || !available {
			continue
		}
		candidate := api.Model{Name: model.ID, Backend: backend}
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
