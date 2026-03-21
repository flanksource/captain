package container

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/huh"
	"github.com/flanksource/captain/pkg/sandbox"
	"github.com/flanksource/captain/pkg/sandbox/presets"
	"golang.org/x/term"
)

const maxLabelWidth = 72

func termHeight() int {
	_, h, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || h < 10 {
		return 20
	}
	return h - 4
}

type WizardResult struct {
	Components     []Component
	Presets        []string
	Tokens         *sandbox.TokensConfig
	Env            map[string]string
	EnvPassthrough []string
}

func RunWizard(components []Component) (*WizardResult, error) {
	h := termHeight()

	var selectedPresets []string
	presetNames := presets.List()
	presetOptions := make([]huh.Option[string], len(presetNames))
	for i, name := range presetNames {
		presetOptions[i] = huh.NewOption(name, name)
	}

	grouped := GroupByCategory(components)

	agentKeys := preselectedKeys(grouped[CategoryAgents])
	agentKeys = append(agentKeys, preselectedKeys(grouped[CategorySkills])...)
	agentOpts := buildCategoryOptions(grouped[CategoryAgents])
	agentOpts = append(agentOpts, buildCategoryOptions(grouped[CategorySkills])...)

	projectKeys := preselectedKeys(grouped[CategoryProjects])
	projectOpts := buildCategoryOptions(grouped[CategoryProjects])

	mcpKeys := preselectedKeys(grouped[CategoryMCPServers])
	mcpOpts := buildCategoryOptions(grouped[CategoryMCPServers])

	// Settings page: split permissions/sandbox toggles from the rest
	var permissionsAllowAll, sandboxDisable bool
	var hasPermissions, hasSandbox bool
	settingsCategories := []Category{
		CategorySettings, CategoryAuth, CategoryFeatureFlags,
		CategoryHooks, CategoryCommands, CategoryPlugins, CategoryClaudeMD,
	}
	var settingsKeys []string
	var settingsOpts []huh.Option[string]
	for _, cat := range settingsCategories {
		for _, c := range grouped[cat] {
			if c.ContentKey == "permissions" || c.ContentKey == "sandbox" {
				if c.ContentKey == "permissions" {
					hasPermissions = true
					if c.Selected && c.OptionValue == "allow-all" {
						permissionsAllowAll = true
					}
				}
				if c.ContentKey == "sandbox" {
					hasSandbox = true
					if c.Selected && c.OptionValue == "off" {
						sandboxDisable = true
					}
				}
				continue
			}
			if c.Selected {
				settingsKeys = append(settingsKeys, c.String())
			}
			label := c.Name
			if c.Description != "" {
				label += " - " + c.Description
			}
			settingsOpts = append(settingsOpts, huh.NewOption(truncateRunes(label, maxLabelWidth), c.String()))
		}
	}

	var selectedTokens []string
	tokenOptions := []huh.Option[string]{
		huh.NewOption("AWS", "aws"),
		huh.NewOption("GCP", "gcp"),
		huh.NewOption("Azure", "azure"),
		huh.NewOption("GitHub", "github"),
		huh.NewOption("Kubernetes", "kubernetes"),
	}

	var envInput string
	var selectedPassthrough []string

	groups := []*huh.Group{
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("1/7 Presets").
				Description("Language/tool presets (env vars, cache volumes, network domains)").
				Options(presetOptions...).
				Value(&selectedPresets).
				Height(h).
				Filterable(true),
		),
	}

	if len(agentOpts) > 0 {
		groups = append(groups, huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("2/7 Agents & Skills").
				Options(agentOpts...).
				Value(&agentKeys).
				Height(h).
				Filterable(true),
		))
	}

	if len(projectOpts) > 0 {
		groups = append(groups, huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("3/7 Projects").
				Description("Project context and working trees to mount").
				Options(projectOpts...).
				Value(&projectKeys).
				Height(h).
				Filterable(true),
		))
	}

	if len(mcpOpts) > 0 {
		groups = append(groups, huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("4/7 MCP Servers").
				Description("MCP servers from ~/.claude.json").
				Options(mcpOpts...).
				Value(&mcpKeys).
				Height(h).
				Filterable(true),
		))
	}

	if hasPermissions || hasSandbox || len(settingsOpts) > 0 {
		var settingsFields []huh.Field
		if hasPermissions {
			settingsFields = append(settingsFields, huh.NewConfirm().
				Title("Permissions: Allow All").
				Description("Grant all tool permissions (no = keep current rules)").
				Value(&permissionsAllowAll))
		}
		if hasSandbox {
			settingsFields = append(settingsFields, huh.NewConfirm().
				Title("Sandbox: Disable").
				Description("Disable sandbox in container (no = keep current config)").
				Value(&sandboxDisable))
		}
		if len(settingsOpts) > 0 {
			settingsFields = append(settingsFields, huh.NewMultiSelect[string]().
				Title("5/7 Settings & Config").
				Description("Settings, auth, hooks, commands, plugins, CLAUDE.md").
				Options(settingsOpts...).
				Value(&settingsKeys).
				Height(h).
				Filterable(true))
		}
		groups = append(groups, huh.NewGroup(settingsFields...))
	}

	groups = append(groups,
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("6/7 Token Providers").
				Description("Cloud credentials to inject into the container").
				Options(tokenOptions...).
				Value(&selectedTokens),
		),
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("7/7 Env Passthrough").
				Description("Host env vars to forward into the container").
				Options(EnvVarOptions()...).
				Value(&selectedPassthrough).
				Height(h).
				Filterable(true),
		),
	)

	form := huh.NewForm(groups...).WithShowHelp(true)
	if err := form.Run(); err != nil {
		return nil, err
	}

	// Merge all selections back into components
	allSelected := make(map[string]bool)
	for _, lists := range [][]string{agentKeys, projectKeys, mcpKeys, settingsKeys} {
		for _, k := range lists {
			allSelected[k] = true
		}
	}
	for i := range components {
		c := &components[i]
		if c.ContentKey == "permissions" {
			c.Selected = (permissionsAllowAll && c.OptionValue == "allow-all") ||
				(!permissionsAllowAll && c.OptionValue == "keep")
			continue
		}
		if c.ContentKey == "sandbox" {
			c.Selected = (sandboxDisable && c.OptionValue == "off") ||
				(!sandboxDisable && c.OptionValue == "keep")
			continue
		}
		c.Selected = allSelected[c.String()]
	}

	result := &WizardResult{
		Components: components,
		Presets:    selectedPresets,
	}

	result.Tokens = buildTokensConfig(selectedTokens)

	result.Env = make(map[string]string)
	for _, line := range strings.Split(envInput, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, "="); ok && k != "" {
			result.Env[k] = v
		}
	}

	result.EnvPassthrough = selectedPassthrough

	return result, nil
}

func buildTokensConfig(selected []string) *sandbox.TokensConfig {
	if len(selected) == 0 {
		return nil
	}
	has := make(map[string]bool, len(selected))
	for _, s := range selected {
		has[s] = true
	}
	tc := &sandbox.TokensConfig{}
	if has["aws"] {
		tc.AWS = &sandbox.AWSTokenConfig{}
	}
	if has["gcp"] {
		tc.GCP = &sandbox.GCPTokenConfig{}
	}
	if has["azure"] {
		tc.Azure = &sandbox.AzureTokenConfig{}
	}
	if has["github"] {
		tc.GitHub = &sandbox.GitHubTokenConfig{}
	}
	if has["kubernetes"] {
		tc.Kubernetes = &sandbox.K8sTokenConfig{}
	}
	return tc
}

// SelectComponents is the simple single-page picker (used by non-interactive flows).
func SelectComponents(components []Component) ([]Component, error) {
	options := buildAllOptions(components)
	selected := preselectedKeys(components)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Select components").
				Options(options...).
				Value(&selected).
				Height(termHeight()).
				Filterable(true),
		),
	).WithShowHelp(true)

	if err := form.Run(); err != nil {
		return nil, err
	}

	applyFormSelection(components, selected)
	return components, nil
}

func RunTUI() error {
	cfg := DefaultDiscoverConfig()
	components := DiscoverAll(cfg)

	sandboxPath := SandboxConfigPath()
	if sb, err := LoadSandboxConfig(sandboxPath); err == nil {
		ApplySelections(components, sb.Components, sb.Options)
	} else {
		ApplyDefaults(components, cfg.LocalDepPaths)
	}

	components, err := SelectComponents(components)
	if err != nil {
		return err
	}

	user := DetectHostUser()
	sb := BuildSandboxConfig(ModeCopy, "claude-env:base", components, user, nil)
	if err := SaveSandboxConfig(sandboxPath, sb); err != nil {
		return fmt.Errorf("saving sandbox config: %w", err)
	}

	fmt.Printf("Saved %d components to %s\n", CountSelected(components), sandboxPath)
	PrintRunInstructions(sandboxPath)
	return nil
}

func buildCategoryOptions(components []Component) []huh.Option[string] {
	var options []huh.Option[string]
	for _, c := range components {
		label := c.Name
		if c.Description != "" {
			label += " - " + c.Description
		}
		label = truncateRunes(label, maxLabelWidth)
		options = append(options, huh.NewOption(label, c.String()))
	}
	return options
}

func buildAllOptions(components []Component) []huh.Option[string] {
	var options []huh.Option[string]
	for _, c := range components {
		label := fmt.Sprintf("[%s] %s", c.Category, c.Name)
		label = truncateRunes(label, maxLabelWidth)
		options = append(options, huh.NewOption(label, c.String()))
	}
	return options
}

func preselectedKeys(components []Component) []string {
	var keys []string
	for _, c := range components {
		if c.Selected {
			keys = append(keys, c.String())
		}
	}
	return keys
}

func truncateRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max-3]) + "..."
}

func applyFormSelection(components []Component, selected []string) {
	selSet := make(map[string]bool, len(selected))
	for _, s := range selected {
		selSet[s] = true
	}
	for i := range components {
		components[i].Selected = selSet[components[i].String()]
	}
}

func EnvVarOptions() []huh.Option[string] {
	env := os.Environ()
	sort.Strings(env)
	options := make([]huh.Option[string], 0, len(env))
	for _, e := range env {
		key, val, _ := strings.Cut(e, "=")
		label := key
		if val != "" {
			preview := val
			if len(preview) > maxLabelWidth-len(key)-1 {
				preview = preview[:maxLabelWidth-len(key)-4] + "..."
			}
			label = key + "=" + preview
		}
		options = append(options, huh.NewOption(label, key))
	}
	return options
}
