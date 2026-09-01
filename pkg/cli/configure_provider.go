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
	Mode     string `json:"mode" pretty:"label=Mode"`
	Model    string `json:"model" pretty:"label=Model"`
	Effort   string `json:"effort,omitempty" pretty:"label=Effort"`
	Active   bool   `json:"active" pretty:"label=Active Provider"`
}

var (
	configureTokenModels    = ai.ListModelsWithAPIKey
	configureDefaultsModels = modelsForRuntime
)

func runProviderConfigure(ctx context.Context, opts ConfigureOptions) (any, error) {
	provider, known := api.ProviderByName(strings.TrimSpace(opts.Provider))
	if !known {
		return nil, fmt.Errorf("provider must be one of: %s (got %q)", api.ProviderList(), opts.Provider)
	}
	hasDefaults := opts.Mode != "" || opts.Model != "" || opts.Effort != "" || opts.Active
	if hasDefaults {
		if !opts.Token.IsEmpty() || opts.Test {
			return nil, fmt.Errorf("token flags cannot be combined with mode, model, effort, or active defaults")
		}
		return runProviderDefaultsConfigure(ctx, provider, opts)
	}
	return runProviderTokenConfigure(ctx, provider, opts)
}

func runProviderDefaultsConfigure(ctx context.Context, provider *api.ModelProvider, opts ConfigureOptions) (ConfigureProviderDefaultsResult, error) {
	saved, _, err := captainconfig.Load()
	if err != nil {
		return ConfigureProviderDefaultsResult{}, err
	}
	current, err := effectiveProviderDefaults(saved.AI, provider)
	if err != nil {
		return ConfigureProviderDefaultsResult{}, err
	}
	if opts.Active && opts.Mode == "" && opts.Model == "" && opts.Effort == "" {
		if err := captainconfig.Update(func(cfg *captainconfig.Config) error {
			cfg.AI.DefaultProvider = provider.Name
			return nil
		}); err != nil {
			return ConfigureProviderDefaultsResult{}, err
		}
		path, _ := captainconfig.Path()
		return ConfigureProviderDefaultsResult{
			Path: path, Provider: provider.Name, Mode: current.Mode,
			Model: current.Model, Effort: current.Effort, Active: true,
		}, nil
	}
	next := current
	selectionChanged := false
	if opts.Mode != "" {
		next.Mode = strings.TrimSpace(opts.Mode)
		if next.Mode != current.Mode && opts.Model == "" {
			next.Model = defaultModelFor(provider, api.RuntimeMode(next.Mode))
		}
		selectionChanged = next.Mode != current.Mode
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
		next.Effort = defaultEffortFor(provider, api.RuntimeMode(next.Mode), next.Model)
	}
	if err := validateProviderDefaults(ctx, provider, next); err != nil {
		return ConfigureProviderDefaultsResult{}, err
	}
	if err := captainconfig.Update(func(cfg *captainconfig.Config) error {
		if cfg.AI.Providers == nil {
			cfg.AI.Providers = map[string]captainconfig.ProviderDefaults{}
		}
		cfg.AI.Providers[provider.Name] = captainconfig.ProviderDefaults{
			Mode: next.Mode, Model: next.Model, ReasoningEffort: next.Effort,
		}
		if opts.Active {
			cfg.AI.DefaultProvider = provider.Name
		}
		return nil
	}); err != nil {
		return ConfigureProviderDefaultsResult{}, err
	}
	path, _ := captainconfig.Path()
	return ConfigureProviderDefaultsResult{
		Path: path, Provider: provider.Name, Mode: next.Mode, Model: next.Model,
		Effort: next.Effort, Active: opts.Active || saved.AI.ActiveProvider() == provider.Name,
	}, nil
}

func defaultEffortFor(provider *api.ModelProvider, mode api.RuntimeMode, model string) string {
	_, effort, ok := ai.ModelEfforts(provider, mode, model)
	if !ok {
		return ""
	}
	return string(effort)
}

func validateProviderDefaults(ctx context.Context, provider *api.ModelProvider, defaults ProviderDefaultView) error {
	mode, ok := api.ParseRuntimeMode(strings.TrimSpace(defaults.Mode))
	if !ok {
		return fmt.Errorf("mode must be one of: %s (got %q)", api.RuntimeModeList(), defaults.Mode)
	}
	if _, serves := provider.Caps(mode); !serves {
		return fmt.Errorf("provider %q has no %s mode (available: %s)", provider.Name, mode, api.RuntimeList())
	}
	runtime := api.RuntimeOf(provider, mode)
	// Saving a default is an explicit choice, so a disabled selection is rejected
	// rather than silently degraded: the fallback chain exists for runs, not for
	// writing a preference the user cannot see is being overridden.
	disabled := ai.Disabled()
	if reason := disabled.Reason(provider, mode); reason != "" {
		return fmt.Errorf("runtime %s is disabled (%s); re-enable it before making it a default", runtime, reason)
	}
	if disabled.Model(provider, mode, strings.TrimSpace(defaults.Model)) {
		return fmt.Errorf("model %q is disabled on runtime %s; re-enable it before making it a default", defaults.Model, runtime)
	}
	if disabled.Effort(api.Effort(strings.TrimSpace(defaults.Effort))) {
		return fmt.Errorf("reasoning effort %q is disabled; re-enable it before making it a default", defaults.Effort)
	}
	models, err := configureDefaultsModels(ctx, provider, mode)
	if err != nil {
		return fmt.Errorf("list models for %s: %w", runtime, err)
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
		return fmt.Errorf("model %q is not available for runtime %s", model, runtime)
	}
	if err := api.Effort(defaults.Effort).Validate(); err != nil {
		return err
	}
	return nil
}

func modelsForRuntime(ctx context.Context, provider *api.ModelProvider, mode api.RuntimeMode) ([]ai.ModelDef, error) {
	if mode.Kind() == "api" {
		return ai.ListModels(ctx, provider)
	}
	return ai.RegistryModelDefs(provider, mode), nil
}

func runProviderTokenConfigure(ctx context.Context, provider *api.ModelProvider, opts ConfigureOptions) (ConfigureTokenResult, error) {
	vault, err := credentials.DefaultVault()
	if err != nil {
		return ConfigureTokenResult{}, err
	}
	token := strings.TrimSpace(opts.Token.Value())
	source := "candidate"
	if token == "" && opts.Test {
		resolved, err := ai.ResolveAPIKey(provider, api.ModeAPI)
		if err != nil {
			return ConfigureTokenResult{}, err
		}
		token, source = resolved.Token, resolved.Source
		if token == "" {
			return ConfigureTokenResult{}, fmt.Errorf("no credential configured for %s", provider.Name)
		}
	} else if token == "" {
		token, err = promptConfigureToken(provider)
		if err != nil {
			return ConfigureTokenResult{}, err
		}
	}
	models, err := configureTokenModels(ctx, provider, token)
	if err != nil {
		return ConfigureTokenResult{}, fmt.Errorf("validate %s credential: %w", provider.Name, err)
	}
	result := ConfigureTokenResult{
		Path: vault.Path(), Provider: provider.Name, MaskedToken: ai.MaskKey(token),
		Source: source, ModelCount: len(models), Saved: !opts.Test,
	}
	if !opts.Test {
		if err := vault.Set(provider.Name, token); err != nil {
			return ConfigureTokenResult{}, err
		}
		result.Source = credentials.SourceVault
	}
	return result, nil
}

func promptConfigureToken(provider *api.ModelProvider) (string, error) {
	info, err := os.Stdin.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect stdin: %w", err)
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return "", fmt.Errorf("token is required in non-interactive mode; pass --token")
	}
	var token string
	form := huh.NewForm(huh.NewGroup(
		huh.NewInput().Title(fmt.Sprintf("%s API token", provider.Name)).EchoMode(huh.EchoModePassword).
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
