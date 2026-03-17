package cli

import (
	"fmt"

	"github.com/flanksource/captain/pkg/container"
)

type ContainerListOptions struct{}

type ContainerGenerateOptions struct {
	Presets []string `flag:"preset" help:"sandbox-runtime presets (golang, npm, docker, etc.)" short:"p"`
	Base    string   `flag:"base" help:"base Docker image" default:"claude-env:base" short:"b"`
	Mode    string   `flag:"mode" help:"config mode: copy or mount" default:"copy" short:"m"`
}

type ContainerBuildOptions struct {
	Presets []string `flag:"preset" help:"sandbox-runtime presets (golang, npm, docker, etc.)" short:"p"`
	Base    string   `flag:"base" help:"base Docker image" default:"claude-env:base" short:"b"`
	Mode    string   `flag:"mode" help:"config mode: copy or mount" default:"copy" short:"m"`
}

type ContainerRunOptions struct {
	Build  bool   `flag:"build" help:"rebuild image before running"`
	Config string `args:"true" help:"path to sandbox config"`
}

func RunContainerTUI() error {
	return container.RunTUI()
}

func RunContainerList(_ ContainerListOptions) (any, error) {
	components := container.DiscoverAll(container.DefaultDiscoverConfig())
	var currentCat container.Category
	for _, c := range components {
		if c.Category != currentCat {
			currentCat = c.Category
			fmt.Printf("\n%s %s\n", c.Category.Icon(), c.Category)
		}
		desc := ""
		if c.Description != "" {
			desc = " - " + c.Description
		}
		dir := ""
		if c.IsDir {
			dir = "/"
		}
		fmt.Printf("  %s%s%s\n", c.Name, dir, desc)
	}
	return nil, nil
}

func RunContainerGenerate(opts ContainerGenerateOptions) (any, error) {
	user := container.DetectHostUser()
	components := discoverAllSelected()
	tag := container.ImageTag(opts.Presets)

	contextDir, err := container.Generate(container.GenerateInput{
		Name:       tag,
		BaseImage:  opts.Base,
		Mode:       container.Mode(opts.Mode),
		Components: components,
		User:       user,
	})
	if err != nil {
		return nil, err
	}
	fmt.Printf("Generated: %s\n", contextDir)

	sandbox := container.BuildSandboxConfig(container.Mode(opts.Mode), opts.Base, components, user, opts.Presets)
	sandboxPath := container.SandboxConfigPath()
	if err := container.SaveSandboxConfig(sandboxPath, sandbox); err != nil {
		fmt.Printf("warning: could not save sandbox config: %v\n", err)
	}
	container.PrintRunInstructions(sandboxPath)
	return nil, nil
}

func RunContainerBuild(opts ContainerBuildOptions) (any, error) {
	user := container.DetectHostUser()
	components := discoverAllSelected()
	tag := container.ImageTag(opts.Presets)

	contextDir, err := container.Generate(container.GenerateInput{
		Name:       tag,
		BaseImage:  opts.Base,
		Mode:       container.Mode(opts.Mode),
		Components: components,
		User:       user,
	})
	if err != nil {
		return nil, fmt.Errorf("generate: %w", err)
	}

	sandbox := container.BuildSandboxConfig(container.Mode(opts.Mode), opts.Base, components, user, opts.Presets)
	sandboxPath := container.SandboxConfigPath()
	if err := container.SaveSandboxConfig(sandboxPath, sandbox); err != nil {
		fmt.Printf("warning: could not save sandbox config: %v\n", err)
	}

	if err := container.Build(container.BuildInput{
		Tag:        tag,
		ContextDir: contextDir,
		User:       user,
	}); err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}

	container.PrintBuildInstructions(tag, container.Mode(opts.Mode), "")
	container.PrintRunInstructions(sandboxPath)
	return nil, nil
}

func RunContainerRun(opts ContainerRunOptions) (any, error) {
	configPath := container.SandboxConfigPath()
	if opts.Config != "" {
		configPath = opts.Config
	}

	cfg, err := container.LoadSandboxConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("loading sandbox config: %w\nRun 'captain container build' first", err)
	}

	if opts.Build {
		fmt.Printf("Rebuilding %s...\n", cfg.Image)
		if err := container.RebuildFromSandbox(cfg); err != nil {
			return nil, fmt.Errorf("rebuild: %w", err)
		}
		fmt.Println("Rebuild complete.")
	}

	if err := container.RunSandbox(cfg, nil); err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	return nil, nil
}

func discoverAllSelected() []container.Component {
	components := container.DiscoverAll(container.DefaultDiscoverConfig())
	for i := range components {
		components[i].Selected = true
	}
	return components
}
