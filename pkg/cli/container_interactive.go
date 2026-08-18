package cli

import (
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/flanksource/captain/pkg/container"
	"github.com/flanksource/captain/pkg/sandbox"
	"github.com/flanksource/captain/pkg/sandbox/presets"
)

func InteractiveRunConfig(cfg *container.SandboxConfig) error {
	var selectedTokens []string
	var selectedPresets []string
	var envInput string
	var selectedPassthrough []string

	tokenProviders := sandbox.TokenProviders()
	tokenOptions := make([]huh.Option[string], len(tokenProviders))
	for i, provider := range tokenProviders {
		tokenOptions[i] = huh.NewOption(provider.Label, provider.Name)
	}

	presetNames := presets.List()
	presetOptions := make([]huh.Option[string], len(presetNames))
	for i, name := range presetNames {
		presetOptions[i] = huh.NewOption(name, name)
	}

	// Pre-select already-configured values
	selectedTokens = append(selectedTokens, sandbox.SelectedTokenProviders(cfg.Tokens)...)
	selectedPresets = append(selectedPresets, cfg.Presets...)
	selectedPassthrough = append(selectedPassthrough, cfg.EnvPassthrough...)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Token providers").
				Description("Select cloud credentials to inject").
				Options(tokenOptions...).
				Value(&selectedTokens),
			huh.NewMultiSelect[string]().
				Title("Presets").
				Description("Select sandbox-runtime presets").
				Options(presetOptions...).
				Value(&selectedPresets).
				Filterable(true),
		),
		huh.NewGroup(
			huh.NewText().
				Title("Environment variables").
				Description("KEY=VALUE per line").
				Value(&envInput),
			huh.NewMultiSelect[string]().
				Title("Env passthrough").
				Description("Host env vars to forward into the container").
				Options(container.EnvVarKeyOptions()...).
				Value(&selectedPassthrough).
				Height(20).
				Filterable(true),
		),
	)

	if err := form.Run(); err != nil {
		return err
	}

	cfg.Tokens = sandbox.ApplyTokenSelection(cfg.Tokens, selectedTokens)
	cfg.Presets = selectedPresets

	for _, line := range strings.Split(envInput, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, "="); ok {
			if cfg.Env == nil {
				cfg.Env = make(map[string]string)
			}
			cfg.Env[k] = v
		}
	}

	cfg.EnvPassthrough = selectedPassthrough

	return nil
}
