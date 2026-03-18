package container

import (
	"fmt"
	"unicode/utf8"

	"github.com/charmbracelet/huh"
)

const maxLabelWidth = 72

func SelectComponents(components []Component) ([]Component, error) {
	options := buildOptions(components)
	selected := preselectedKeys(components)

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Select components").
				Options(options...).
				Value(&selected).
				Height(20).
				Filterable(true),
		),
	)

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
	if sandbox, err := LoadSandboxConfig(sandboxPath); err == nil {
		ApplySelections(components, sandbox.Components, sandbox.Options)
	} else {
		ApplyDefaults(components, cfg.LocalDepPaths)
	}

	components, err := SelectComponents(components)
	if err != nil {
		return err
	}

	user := DetectHostUser()
	sandbox := BuildSandboxConfig(ModeCopy, "claude-env:base", components, user, nil)
	if err := SaveSandboxConfig(sandboxPath, sandbox); err != nil {
		return fmt.Errorf("saving sandbox config: %w", err)
	}

	fmt.Printf("Saved %d components to %s\n", CountSelected(components), sandboxPath)
	PrintRunInstructions(sandboxPath)
	return nil
}

func buildOptions(components []Component) []huh.Option[string] {
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
