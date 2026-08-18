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
	"github.com/flanksource/clicky"
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

func RunWizard(components []Component, existing *SandboxConfig) (*WizardResult, error) {
	h := termHeight()

	var selectedPresets []string
	if existing != nil {
		selectedPresets = existing.Presets
	}
	presetNames := presets.List()
	presetOptions := make([]huh.Option[string], len(presetNames))
	for i, name := range presetNames {
		presetOptions[i] = huh.NewOption(name, name)
	}

	grouped := GroupByCategory(components)

	agentKeys := preselectedKeys(grouped[CategoryAgents])
	agentOpts := sortedCategoryOptions(grouped[CategoryAgents])

	skillKeys := preselectedKeys(grouped[CategorySkills])
	skillOpts := sortedCategoryOptions(grouped[CategorySkills])

	pluginKeys := preselectedKeys(grouped[CategoryPlugins])
	pluginOpts := sortedCategoryOptions(grouped[CategoryPlugins])

	projectKeys := preselectedKeys(grouped[CategoryProjects])
	projectOpts := sortedCategoryOptions(grouped[CategoryProjects])

	mcpKeys := preselectedKeys(grouped[CategoryMCPServers])
	mcpOpts := sortedCategoryOptions(grouped[CategoryMCPServers])

	// Settings page: split permissions/sandbox toggles from the rest
	var permissionsAllowAll, sandboxDisable bool
	var hasPermissions, hasSandbox bool
	settingsCategories := []Category{
		CategorySettings, CategoryAuth, CategoryFeatureFlags,
		CategoryHooks, CategoryCommands, CategoryClaudeMD,
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
	if existing != nil {
		selectedTokens = sandbox.SelectedTokenProviders(existing.Tokens)
	}
	tokenProviders := sandbox.TokenProviders()
	tokenOptions := make([]huh.Option[string], len(tokenProviders))
	for i, provider := range tokenProviders {
		tokenOptions[i] = huh.NewOption(provider.Label, provider.Name)
	}

	var envKeys []string
	if existing != nil {
		for k := range existing.Env {
			envKeys = append(envKeys, k)
		}
		sort.Strings(envKeys)
	}
	var selectedPassthrough []string
	if existing != nil {
		selectedPassthrough = existing.EnvPassthrough
	}

	page := 0
	groups := []*huh.Group{
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title(pageTitle(&page, "Presets")).
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
				Title(pageTitle(&page, "Agents")).
				Options(agentOpts...).
				Value(&agentKeys).
				Height(h).
				Filterable(true),
		))
	}

	if len(skillOpts) > 0 {
		groups = append(groups, huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title(pageTitle(&page, "Skills")).
				Options(skillOpts...).
				Value(&skillKeys).
				Height(h).
				Filterable(true),
		))
	}

	if len(pluginOpts) > 0 {
		groups = append(groups, huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title(pageTitle(&page, "Plugins")).
				Options(pluginOpts...).
				Value(&pluginKeys).
				Height(h).
				Filterable(true),
		))
	}

	if len(projectOpts) > 0 {
		groups = append(groups, huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title(pageTitle(&page, "Projects")).
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
				Title(pageTitle(&page, "MCP Servers")).
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
				Title(pageTitle(&page, "Settings & Config")).
				Description("Settings, auth, hooks, commands, CLAUDE.md").
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
				Title(pageTitle(&page, "Token Providers")).
				Description("Cloud credentials to inject into the container").
				Options(tokenOptions...).
				Value(&selectedTokens),
		),
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title(pageTitle(&page, "Env Passthrough")).
				Description("Host env var names to forward into the container (values loaded at runtime)").
				Options(EnvVarKeyOptions()...).
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
	for _, lists := range [][]string{agentKeys, skillKeys, pluginKeys, projectKeys, mcpKeys, settingsKeys} {
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

	result.Tokens = sandbox.ApplyTokenSelection(nil, selectedTokens)

	result.Env = make(map[string]string)
	if existing != nil {
		for k, v := range existing.Env {
			result.Env[k] = v
		}
	}
	for _, k := range envKeys {
		if _, exists := result.Env[k]; !exists {
			result.Env[k] = ""
		}
	}

	result.EnvPassthrough = selectedPassthrough

	return result, nil
}

func pageTitle(counter *int, name string) string {
	*counter++
	return fmt.Sprintf("%d. %s", *counter, name)
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

	clicky.Printf("Saved %d components to %s\n", CountSelected(components), sandboxPath)
	PrintRunInstructions(sandboxPath)
	return nil
}

func sortedCategoryOptions(components []Component) []huh.Option[string] {
	sorted := make([]Component, len(components))
	copy(sorted, components)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})
	return buildCategoryOptions(sorted)
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

func EnvVarKeyOptions() []huh.Option[string] {
	env := os.Environ()
	keys := make([]string, 0, len(env))
	for _, e := range env {
		key, _, _ := strings.Cut(e, "=")
		keys = append(keys, key)
	}
	sort.Strings(keys)
	options := make([]huh.Option[string], len(keys))
	for i, k := range keys {
		options[i] = huh.NewOption(k, k)
	}
	return options
}
