package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

type ProviderDefaultView struct {
	Agent      string `json:"agent"`
	Model      string `json:"model"`
	Effort     string `json:"effort"`
	Configured bool   `json:"configured"`
}

func effectiveProviderDefaults(saved captainconfig.AIDefaults, provider api.Backend) (ProviderDefaultView, error) {
	if provider.Provider() != provider {
		return ProviderDefaultView{}, fmt.Errorf("invalid provider %q", provider)
	}
	configured, exists := saved.Providers[string(provider)]
	legacy := saved.Provider(string(provider))
	if configured.Agent == "" {
		configured.Agent = legacy.Agent
	}
	if configured.Model == "" {
		configured.Model = legacy.Model
	}
	if configured.ReasoningEffort == "" {
		configured.ReasoningEffort = legacy.ReasoningEffort
	}
	agent := api.Backend(strings.TrimSpace(configured.Agent))
	if agent == "" {
		agent = provider
	}
	if agent.Provider() != provider {
		return ProviderDefaultView{}, fmt.Errorf("agent %q does not belong to provider %q", agent, provider)
	}
	model := strings.TrimSpace(configured.Model)
	if model == "" {
		model = defaultModelFor(agent)
	}
	effort := api.Effort(strings.TrimSpace(configured.ReasoningEffort))
	if err := effort.Validate(); err != nil {
		return ProviderDefaultView{}, err
	}
	return ProviderDefaultView{
		Agent: string(agent), Model: model, Effort: string(effort), Configured: exists,
	}, nil
}

func applyProviderDefaults(model api.Model, saved captainconfig.AIDefaults) (api.Model, error) {
	var err error
	if strings.TrimSpace(model.Name) != "" {
		model, err = model.Expand()
		if err != nil {
			return api.Model{}, err
		}
	}
	model, err = applyCandidateDefaults(model, saved, true)
	if err != nil {
		return api.Model{}, err
	}
	for i := range model.Fallbacks {
		fallback := model.Fallbacks[i]
		fallback, err = fallback.Expand()
		if err != nil {
			return api.Model{}, fmt.Errorf("fallback[%d]: %w", i, err)
		}
		fallback, err = applyCandidateDefaults(fallback, saved, false)
		if err != nil {
			return api.Model{}, fmt.Errorf("fallback[%d]: %w", i, err)
		}
		model.Fallbacks[i] = fallback
	}
	return model, nil
}

func applyCandidateDefaults(model api.Model, saved captainconfig.AIDefaults, allowActive bool) (api.Model, error) {
	provider := model.Backend.Provider()
	if provider == "" && strings.TrimSpace(model.Name) != "" {
		backend, err := api.InferBackend(model.Name)
		if err != nil {
			return api.Model{}, err
		}
		provider = backend.Provider()
	}
	if provider == "" && allowActive {
		provider = api.Backend(saved.ActiveProvider())
	}
	if provider == "" {
		return api.Model{}, fmt.Errorf("provider cannot be resolved for model %q", model.Name)
	}
	defaults, err := effectiveProviderDefaults(saved, provider)
	if err != nil {
		return api.Model{}, err
	}
	if model.Backend == "" && model.Mode == "" {
		model.Backend = api.Backend(defaults.Agent)
	}
	if strings.TrimSpace(model.Name) == "" {
		model.Name = defaults.Model
	}
	if model.Effort == api.EffortNone {
		model.Effort = api.Effort(defaults.Effort)
	}
	if err := model.Effort.Validate(); err != nil {
		return api.Model{}, err
	}
	return model, nil
}

func allProviderDefaults(saved captainconfig.AIDefaults) (map[string]ProviderDefaultView, error) {
	providers := []api.Backend{api.AnthropicProvider, api.OpenAIProvider, api.GeminiProvider, api.DeepSeekProvider}
	out := make(map[string]ProviderDefaultView, len(providers))
	for _, provider := range providers {
		defaults, err := effectiveProviderDefaults(saved, provider)
		if err != nil {
			return nil, err
		}
		out[string(provider)] = defaults
	}
	return out, nil
}
