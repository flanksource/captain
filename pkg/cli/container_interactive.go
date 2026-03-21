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

	tokenOptions := []huh.Option[string]{
		huh.NewOption("AWS", "aws"),
		huh.NewOption("GCP", "gcp"),
		huh.NewOption("Azure", "azure"),
		huh.NewOption("GitHub", "github"),
		huh.NewOption("Kubernetes", "kubernetes"),
	}

	presetNames := presets.List()
	presetOptions := make([]huh.Option[string], len(presetNames))
	for i, name := range presetNames {
		presetOptions[i] = huh.NewOption(name, name)
	}

	// Pre-select already-configured values
	if cfg.Tokens != nil {
		if cfg.Tokens.AWS != nil {
			selectedTokens = append(selectedTokens, "aws")
		}
		if cfg.Tokens.GCP != nil {
			selectedTokens = append(selectedTokens, "gcp")
		}
		if cfg.Tokens.Azure != nil {
			selectedTokens = append(selectedTokens, "azure")
		}
		if cfg.Tokens.GitHub != nil {
			selectedTokens = append(selectedTokens, "github")
		}
		if cfg.Tokens.Kubernetes != nil {
			selectedTokens = append(selectedTokens, "kubernetes")
		}
	}
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
				Options(container.EnvVarOptions()...).
				Value(&selectedPassthrough).
				Height(20).
				Filterable(true),
		),
	)

	if err := form.Run(); err != nil {
		return err
	}

	applyTokenSelections(cfg, selectedTokens)
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

func applyTokenSelections(cfg *container.SandboxConfig, selected []string) {
	if len(selected) == 0 {
		cfg.Tokens = nil
		return
	}

	if cfg.Tokens == nil {
		cfg.Tokens = &sandbox.TokensConfig{}
	}

	has := make(map[string]bool, len(selected))
	for _, s := range selected {
		has[s] = true
	}

	if has["aws"] && cfg.Tokens.AWS == nil {
		cfg.Tokens.AWS = &sandbox.AWSTokenConfig{}
	} else if !has["aws"] {
		cfg.Tokens.AWS = nil
	}

	if has["gcp"] && cfg.Tokens.GCP == nil {
		cfg.Tokens.GCP = &sandbox.GCPTokenConfig{}
	} else if !has["gcp"] {
		cfg.Tokens.GCP = nil
	}

	if has["azure"] && cfg.Tokens.Azure == nil {
		cfg.Tokens.Azure = &sandbox.AzureTokenConfig{}
	} else if !has["azure"] {
		cfg.Tokens.Azure = nil
	}

	if has["github"] && cfg.Tokens.GitHub == nil {
		cfg.Tokens.GitHub = &sandbox.GitHubTokenConfig{}
	} else if !has["github"] {
		cfg.Tokens.GitHub = nil
	}

	if has["kubernetes"] && cfg.Tokens.Kubernetes == nil {
		cfg.Tokens.Kubernetes = &sandbox.K8sTokenConfig{}
	} else if !has["kubernetes"] {
		cfg.Tokens.Kubernetes = nil
	}
}
