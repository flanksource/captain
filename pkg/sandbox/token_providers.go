package sandbox

// The provider table. Every surface that lets a user choose token providers —
// the container wizard, the interactive run config, and Acquire itself — ranges
// over this one slice.
//
// It replaces four parallel hardcoded lists (a huh.NewOption slice and a
// set/clear switch in each of pkg/container/tui.go and
// pkg/cli/container_interactive.go), where adding a provider meant editing all
// four and a miss showed up as a provider that could be selected but never
// acquired.

// TokenProvider describes one acquirable credential source.
type TokenProvider struct {
	// Name is the configuration token (`tokens: {github: {}}`) and the value
	// carried by selection widgets.
	Name string
	// Label is how the provider is shown to a human.
	Label string
	// Enabled reports whether a config already selects this provider.
	Enabled func(*TokensConfig) bool
	// Set selects the provider, leaving any existing settings alone.
	Set func(*TokensConfig)
	// Clear deselects the provider.
	Clear func(*TokensConfig)
}

// TokenProviders returns every selectable provider in display order.
func TokenProviders() []TokenProvider {
	return []TokenProvider{
		{
			Name: "aws", Label: "AWS",
			Enabled: func(c *TokensConfig) bool { return c.AWS != nil },
			Set: func(c *TokensConfig) {
				if c.AWS == nil {
					c.AWS = &AWSTokenConfig{}
				}
			},
			Clear: func(c *TokensConfig) { c.AWS = nil },
		},
		{
			Name: "gcp", Label: "GCP",
			Enabled: func(c *TokensConfig) bool { return c.GCP != nil },
			Set: func(c *TokensConfig) {
				if c.GCP == nil {
					c.GCP = &GCPTokenConfig{}
				}
			},
			Clear: func(c *TokensConfig) { c.GCP = nil },
		},
		{
			Name: "azure", Label: "Azure",
			Enabled: func(c *TokensConfig) bool { return c.Azure != nil },
			Set: func(c *TokensConfig) {
				if c.Azure == nil {
					c.Azure = &AzureTokenConfig{}
				}
			},
			Clear: func(c *TokensConfig) { c.Azure = nil },
		},
		{
			Name: "github", Label: "GitHub",
			Enabled: func(c *TokensConfig) bool { return c.GitHub != nil },
			Set: func(c *TokensConfig) {
				if c.GitHub == nil {
					c.GitHub = &GitHubTokenConfig{}
				}
			},
			Clear: func(c *TokensConfig) { c.GitHub = nil },
		},
		{
			Name: "kubernetes", Label: "Kubernetes",
			Enabled: func(c *TokensConfig) bool { return c.Kubernetes != nil },
			Set: func(c *TokensConfig) {
				if c.Kubernetes == nil {
					c.Kubernetes = &K8sTokenConfig{}
				}
			},
			Clear: func(c *TokensConfig) { c.Kubernetes = nil },
		},
		{
			Name: "claude", Label: "Claude (subscription login)",
			Enabled: func(c *TokensConfig) bool { return c.Claude != nil },
			Set: func(c *TokensConfig) {
				if c.Claude == nil {
					c.Claude = &ClaudeTokenConfig{}
				}
			},
			Clear: func(c *TokensConfig) { c.Claude = nil },
		},
		{
			Name: "codex", Label: "Codex (subscription login)",
			Enabled: func(c *TokensConfig) bool { return c.Codex != nil },
			Set: func(c *TokensConfig) {
				if c.Codex == nil {
					c.Codex = &CodexTokenConfig{}
				}
			},
			Clear: func(c *TokensConfig) { c.Codex = nil },
		},
	}
}

// SelectedTokenProviders lists the provider names a config enables.
func SelectedTokenProviders(config *TokensConfig) []string {
	if config == nil {
		return nil
	}
	var selected []string
	for _, provider := range TokenProviders() {
		if provider.Enabled(config) {
			selected = append(selected, provider.Name)
		}
	}
	return selected
}

// ApplyTokenSelection converges config onto exactly the named providers,
// preserving the settings of providers that stay selected. A nil config with an
// empty selection stays nil, so an untouched wizard does not write an empty
// tokens block into a user's file.
func ApplyTokenSelection(config *TokensConfig, selected []string) *TokensConfig {
	if len(selected) == 0 {
		return nil
	}
	if config == nil {
		config = &TokensConfig{}
	}
	chosen := make(map[string]bool, len(selected))
	for _, name := range selected {
		chosen[name] = true
	}
	for _, provider := range TokenProviders() {
		if chosen[provider.Name] {
			provider.Set(config)
			continue
		}
		provider.Clear(config)
	}
	return config
}
