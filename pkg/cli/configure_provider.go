package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/credentials"
)

type ConfigureTokenResult struct {
	Path        string `json:"path" pretty:"label=Vault"`
	Provider    string `json:"provider" pretty:"label=Provider"`
	MaskedToken string `json:"maskedToken" pretty:"label=Token"`
	Source      string `json:"source" pretty:"label=Source"`
	ModelCount  int    `json:"modelCount" pretty:"label=Models"`
	Saved       bool   `json:"saved" pretty:"label=Saved"`
}

type ConfigureProviderDefaultsResult struct {
	Path     string `json:"path" pretty:"label=Saved To"`
	Provider string `json:"provider" pretty:"label=Provider"`
	Agent    string `json:"agent" pretty:"label=Agent"`
	Model    string `json:"model" pretty:"label=Model"`
	Effort   string `json:"effort,omitempty" pretty:"label=Effort"`
	Active   bool   `json:"active" pretty:"label=Active Provider"`
}

var (
	configureTokenModels    = ai.ListModelsWithAPIKey
	configureDefaultsModels = modelsForConfigurableAgent
)

func runProviderConfigure(ctx context.Context, opts ConfigureOptions) (any, error) {
	provider := api.Backend(strings.TrimSpace(opts.Provider))
	if !configurableAPIBackend(provider) {
		return nil, fmt.Errorf("provider must be one of: anthropic, openai, gemini, deepseek (got %q)", opts.Provider)
	}
	hasDefaults := opts.Agent != "" || opts.Model != "" || opts.Effort != "" || opts.Active
	if hasDefaults {
		if !opts.Token.IsEmpty() || opts.Test {
			return nil, fmt.Errorf("token flags cannot be combined with agent, model, effort, or active defaults")
		}
		return runProviderDefaultsConfigure(ctx, provider, opts)
	}
	return runProviderTokenConfigure(ctx, provider, opts)
}

func runProviderDefaultsConfigure(ctx context.Context, provider api.Backend, opts ConfigureOptions) (ConfigureProviderDefaultsResult, error) {
	saved, _, err := captainconfig.Load()
	if err != nil {
		return ConfigureProviderDefaultsResult{}, err
	}
	current, err := effectiveProviderDefaults(saved.AI, provider)
	if err != nil {
		return ConfigureProviderDefaultsResult{}, err
	}
	if opts.Active && opts.Agent == "" && opts.Model == "" && opts.Effort == "" {
		if err := captainconfig.Update(func(cfg *captainconfig.Config) error {
			cfg.AI.DefaultProvider = string(provider)
			return nil
		}); err != nil {
			return ConfigureProviderDefaultsResult{}, err
		}
		path, _ := captainconfig.Path()
		return ConfigureProviderDefaultsResult{
			Path: path, Provider: string(provider), Agent: current.Agent,
			Model: current.Model, Effort: current.Effort, Active: true,
		}, nil
	}
	next := current
	selectionChanged := false
	if opts.Agent != "" {
		next.Agent = strings.TrimSpace(opts.Agent)
		if next.Agent != current.Agent && opts.Model == "" {
			next.Model = defaultModelFor(api.Backend(next.Agent))
		}
		selectionChanged = next.Agent != current.Agent
	}
	if opts.Model != "" {
		next.Model = strings.TrimSpace(opts.Model)
		selectionChanged = selectionChanged || next.Model != current.Model
	}
	if opts.Effort != "" {
		if strings.EqualFold(strings.TrimSpace(opts.Effort), "default") {
			next.Effort = ""
		} else {
			next.Effort = strings.TrimSpace(opts.Effort)
		}
	} else if selectionChanged {
		next.Effort = defaultEffortFor(api.Backend(next.Agent), next.Model)
	}
	if err := validateProviderDefaults(ctx, provider, next); err != nil {
		return ConfigureProviderDefaultsResult{}, err
	}
	if err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		if cfg.AI.Providers == nil {
			cfg.AI.Providers = map[string]captainconfig.ProviderDefaults{}
		}
		cfg.AI.Providers[string(provider)] = captainconfig.ProviderDefaults{
			Agent: next.Agent, Model: next.Model, ReasoningEffort: next.Effort,
		}
		if opts.Active {
			cfg.AI.DefaultProvider = string(provider)
		}
		return nil
	}); err != nil {
		return ConfigureProviderDefaultsResult{}, err
	}
	path, _ := captainconfig.Path()
	return ConfigureProviderDefaultsResult{
		Path: path, Provider: string(provider), Agent: next.Agent, Model: next.Model,
		Effort: next.Effort, Active: opts.Active || saved.AI.ActiveProvider() == string(provider),
	}, nil
}

func defaultEffortFor(agent api.Backend, model string) string {
	_, effort, ok := ai.ModelEfforts(agent, model)
	if !ok {
		return ""
	}
	return string(effort)
}

func validateProviderDefaults(ctx context.Context, provider api.Backend, defaults ProviderDefaultView) error {
	agent := api.Backend(strings.TrimSpace(defaults.Agent))
	if agent.Provider() != provider {
		return fmt.Errorf("agent %q does not belong to provider %q", agent, provider)
	}
	models, err := configureDefaultsModels(ctx, agent)
	if err != nil {
		return fmt.Errorf("list models for %s: %w", agent, err)
	}
	model := strings.TrimSpace(defaults.Model)
	found := false
	for _, candidate := range models {
		if candidate.ID == model {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("model %q is not available for agent %q", model, agent)
	}
	if err := ai.ValidateModelEffort(agent, model, api.Effort(defaults.Effort)); err != nil {
		return err
	}
	return nil
}

func modelsForConfigurableAgent(ctx context.Context, agent api.Backend) ([]ai.ModelDef, error) {
	if agent.Kind() == "api" {
		return ai.ListModels(ctx, agent)
	}
	return agentCatalogModels(agent), nil
}

func runProviderTokenConfigure(ctx context.Context, backend api.Backend, opts ConfigureOptions) (ConfigureTokenResult, error) {
	vault, err := credentials.DefaultVault()
	if err != nil {
		return ConfigureTokenResult{}, err
	}
	token := strings.TrimSpace(opts.Token.Value())
	source := "candidate"
	if token == "" && opts.Test {
		resolved, err := ai.ResolveAPIKey(backend)
		if err != nil {
			return ConfigureTokenResult{}, err
		}
		token, source = resolved.Token, resolved.Source
		if token == "" {
			return ConfigureTokenResult{}, fmt.Errorf("no credential configured for %s", backend)
		}
	} else if token == "" {
		token, err = promptConfigureToken(backend)
		if err != nil {
			return ConfigureTokenResult{}, err
		}
	}
	models, err := configureTokenModels(ctx, backend, token)
	if err != nil {
		return ConfigureTokenResult{}, fmt.Errorf("validate %s credential: %w", backend, err)
	}
	result := ConfigureTokenResult{
		Path: vault.Path(), Provider: string(backend), MaskedToken: ai.MaskKey(token),
		Source: source, ModelCount: len(models), Saved: !opts.Test,
	}
	if !opts.Test {
		if err := vault.Set(string(backend), token); err != nil {
			return ConfigureTokenResult{}, err
		}
		result.Source = credentials.SourceVault
	}
	return result, nil
}

func configurableAPIBackend(backend api.Backend) bool {
	return backend.Provider() == backend
}

func promptConfigureToken(backend api.Backend) (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect stdin: %w", err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return "", fmt.Errorf("token is required in non-interactive mode; pass --token")
	}
	var token string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title(fmt.Sprintf("%s API token", backend)).EchoMode(huh.EchoModePassword).
			Value(&token).Validate(func(value string) error {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("token cannot be empty")
			}
			return nil
		}),
	))
	if err := form.Run(); err != nil {
		return "", err
	}
	return strings.TrimSpace(token), nil
}
