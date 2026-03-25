package cli

import (
	"fmt"
	"os"

	"github.com/flanksource/captain/pkg/container"
	"github.com/flanksource/captain/pkg/sandbox/presets"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
)

type ContainerListOptions struct{}

type ContainerGenerateOptions struct {
	Interactive bool     `flag:"interactive" help:"interactively select components" short:"i"`
	Presets     []string `flag:"preset" help:"sandbox-runtime presets (golang, npm, docker, etc.)" short:"p"`
	Base        string   `flag:"base" help:"base Docker image" default:"claude-env:base" short:"b"`
	Mode        string   `flag:"mode" help:"config mode: copy or mount" default:"copy" short:"m"`
}

func (ContainerGenerateOptions) Help() api.Textable { return containerHelp() }

type ContainerBuildOptions struct {
	Interactive bool     `flag:"interactive" help:"interactively select components" short:"i"`
	Presets     []string `flag:"preset" help:"sandbox-runtime presets (golang, npm, docker, etc.)" short:"p"`
	Base        string   `flag:"base" help:"base Docker image" default:"claude-env:base" short:"b"`
	Mode        string   `flag:"mode" help:"config mode: copy or mount" default:"copy" short:"m"`
}

func (ContainerBuildOptions) Help() api.Textable { return containerHelp() }

type ContainerRunOptions struct {
	Build          bool              `flag:"build" help:"rebuild image before running"`
	Config         string            `args:"true" help:"path to sandbox config"`
	Presets        []string          `flag:"preset" help:"sandbox-runtime presets to merge in" short:"p"`
	Env            map[string]string `flag:"env" help:"additional env vars (KEY=VALUE) to pass into the container" short:"e"`
	EnvPassthrough []string          `flag:"env-passthrough" help:"additional host env vars to forward into the container"`
	Name           string            `flag:"name" help:"override container name (--name)"`
	Interactive    bool              `flag:"interactive" help:"interactively select tokens, presets, and env vars" short:"i"`
	Remove         bool              `flag:"rm" help:"stop and remove container after exec exits"`
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
	components := discoverDefaultSelected()

	selectedPresets := opts.Presets
	var wizardResult *container.WizardResult

	if opts.Interactive {
		var err error
		wizardResult, err = container.RunWizard(components)
		if err != nil {
			return nil, err
		}
		components = wizardResult.Components
		selectedPresets = wizardResult.Presets
	}

	base, versions := resolvePresetVersions(selectedPresets, opts.Base)
	tag := container.ImageTag(selectedPresets)

	contextDir, err := container.Generate(container.GenerateInput{
		Name:          tag,
		BaseImage:     base,
		Mode:          container.Mode(opts.Mode),
		Components:    components,
		User:          user,
		PresetInstall: presets.ResolveInstallSnippets(selectedPresets, versions),
	})
	if err != nil {
		return nil, err
	}
	fmt.Printf("Generated: %s\n", contextDir)

	sb := container.BuildSandboxConfig(container.Mode(opts.Mode), base, components, user, selectedPresets)
	if wizardResult != nil {
		sb.Tokens = wizardResult.Tokens
		sb.Env = wizardResult.Env
		sb.EnvPassthrough = wizardResult.EnvPassthrough
	}
	sandboxPath := container.SandboxConfigPath()
	if err := container.SaveSandboxConfig(sandboxPath, sb); err != nil {
		fmt.Printf("warning: could not save sandbox config: %v\n", err)
	}

	buildInput := container.BuildInput{Tag: tag, ContextDir: contextDir, User: user}
	if err := container.GenerateShortcuts(container.ShortcutsInput{Dir: contextDir, Config: sb, BuildArgs: buildInput, Components: components}); err != nil {
		fmt.Printf("warning: could not generate shortcuts: %v\n", err)
	}

	container.PrintRunInstructions(sandboxPath)
	return nil, nil
}

func RunContainerBuild(opts ContainerBuildOptions) (any, error) {
	user := container.DetectHostUser()
	components := discoverDefaultSelected()

	selectedPresets := opts.Presets
	var wizardResult *container.WizardResult

	if opts.Interactive {
		var err error
		wizardResult, err = container.RunWizard(components)
		if err != nil {
			return nil, err
		}
		components = wizardResult.Components
		selectedPresets = wizardResult.Presets
	}

	base, versions := resolvePresetVersions(selectedPresets, opts.Base)

	if err := container.EnsureBaseImage(base); err != nil {
		return nil, fmt.Errorf("base image: %w", err)
	}

	tag := container.ImageTag(selectedPresets)

	contextDir, err := container.Generate(container.GenerateInput{
		Name:          tag,
		BaseImage:     base,
		Mode:          container.Mode(opts.Mode),
		Components:    components,
		User:          user,
		PresetInstall: presets.ResolveInstallSnippets(selectedPresets, versions),
	})
	if err != nil {
		return nil, fmt.Errorf("generate: %w", err)
	}

	sb := container.BuildSandboxConfig(container.Mode(opts.Mode), base, components, user, selectedPresets)
	if wizardResult != nil {
		sb.Tokens = wizardResult.Tokens
		sb.Env = wizardResult.Env
		sb.EnvPassthrough = wizardResult.EnvPassthrough
	}
	sandboxPath := container.SandboxConfigPath()
	if err := container.SaveSandboxConfig(sandboxPath, sb); err != nil {
		fmt.Printf("warning: could not save sandbox config: %v\n", err)
	}

	buildInput := container.BuildInput{Tag: tag, ContextDir: contextDir, User: user}
	if err := container.Build(buildInput); err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}

	if err := container.GenerateShortcuts(container.ShortcutsInput{Dir: contextDir, Config: sb, BuildArgs: buildInput, Components: components}); err != nil {
		fmt.Printf("warning: could not generate shortcuts: %v\n", err)
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

	if len(opts.Presets) > 0 {
		cfg.Presets = append(cfg.Presets, opts.Presets...)
	}
	if len(opts.Env) > 0 {
		if cfg.Env == nil {
			cfg.Env = make(map[string]string)
		}
		for k, v := range opts.Env {
			cfg.Env[k] = v
		}
	}
	if len(opts.EnvPassthrough) > 0 {
		cfg.EnvPassthrough = append(cfg.EnvPassthrough, opts.EnvPassthrough...)
	}
	if opts.Name != "" {
		cfg.Name = opts.Name
	}

	if err := container.EnsureBaseImage(cfg.BaseImage); err != nil {
		return nil, fmt.Errorf("base image: %w", err)
	}

	if opts.Interactive {
		if err := InteractiveRunConfig(&cfg); err != nil {
			return nil, fmt.Errorf("interactive config: %w", err)
		}
	}

	if opts.Build {
		fmt.Printf("Rebuilding %s...\n", cfg.Image)
		if err := container.RebuildFromSandbox(cfg); err != nil {
			return nil, fmt.Errorf("rebuild: %w", err)
		}
		fmt.Println("Rebuild complete.")
	}

	if err := container.RunSandbox(cfg, container.RunOptions{Remove: opts.Remove}); err != nil {
		return nil, fmt.Errorf("run: %w", err)
	}
	return nil, nil
}

func ContainerHelp() api.Text { return containerHelp() }

func containerHelp() api.Text {
	text := clicky.Text("Container sandbox builder for Claude Code", "font-bold text-blue-400").NewLine().NewLine().
		AddText("Discovers Claude Code config (agents, commands, hooks, MCP servers, settings)", "text-gray-400").NewLine().
		AddText("and packages everything into a Docker image. Sandbox-runtime presets add", "text-gray-400").NewLine().
		AddText("language-specific env vars, cache volumes, and network domains.", "text-gray-400").NewLine().NewLine().
		AddText("Workflow:", "font-bold text-blue-400").NewLine().
		AddText("  captain container", "text-green-400").
		AddText("                              # interactive TUI", "text-gray-500").NewLine().
		AddText("  captain container generate", "text-green-400").
		AddText("                          # generate Dockerfile", "text-gray-500").NewLine().
		AddText("  captain container generate -i", "text-green-400").
		AddText("                       # interactive component picker", "text-gray-500").NewLine().
		AddText("  captain container build --preset golang", "text-green-400").
		AddText("      # build image", "text-gray-500").NewLine().
		AddText("  captain container run", "text-green-400").
		AddText("                           # run sandbox", "text-gray-500").NewLine().
		AddText("  captain container run --build", "text-green-400").
		AddText("                    # rebuild + run", "text-gray-500").NewLine().NewLine().
		AddText("generate/build Flags:", "font-bold text-blue-400").NewLine().
		AddText("  -i, --interactive", "text-yellow-400").
		AddText("       interactively select components", "text-gray-400").NewLine().
		AddText("  -p, --preset strings", "text-yellow-400").
		AddText("  sandbox-runtime presets (golang, npm, docker, etc.)", "text-gray-400").NewLine().
		AddText("  -b, --base string", "text-yellow-400").
		AddText("     base Docker image (default: claude-env:base)", "text-gray-400").NewLine().
		AddText("  -m, --mode string", "text-yellow-400").
		AddText("     config mode: copy or mount (default: copy)", "text-gray-400").NewLine().NewLine().
		AddText("run Flags:", "font-bold text-blue-400").NewLine().
		AddText("  -i, --interactive", "text-yellow-400").
		AddText("       interactively select tokens, presets, and env vars", "text-gray-400").NewLine().
		AddText("  --build", "text-yellow-400").
		AddText("                   rebuild image before running", "text-gray-400").NewLine().
		AddText("  --rm", "text-yellow-400").
		AddText("                      stop and remove container after exec exits", "text-gray-400").NewLine().
		AddText("  -p, --preset strings", "text-yellow-400").
		AddText("  merge additional sandbox-runtime presets", "text-gray-400").NewLine().
		AddText("  -e, --env KEY=VALUE", "text-yellow-400").
		AddText("   pass additional env vars into the container", "text-gray-400").NewLine().
		AddText("  --env-passthrough strings", "text-yellow-400").
		AddText(" forward additional host env vars", "text-gray-400").NewLine().
		AddText("  --name string", "text-yellow-400").
		AddText("             override container name (--name passed to docker run)", "text-gray-400").NewLine().NewLine().
		AddText(".container-sandbox.yaml spec:", "font-bold text-blue-400").NewLine().
		AddText("  name: my-sandbox", "text-cyan-400").
		AddText("                    # docker --name for the container", "text-gray-500").NewLine().
		AddText("  image: claude-env:golang", "text-cyan-400").
		AddText("               # Docker image to run", "text-gray-500").NewLine().
		AddText("  baseImage: claude-env:base", "text-cyan-400").
		AddText("           # base image for builds", "text-gray-500").NewLine().
		AddText("  mode: copy|mount", "text-cyan-400").
		AddText("                    # config packaging mode", "text-gray-500").NewLine().
		AddText("  presets: [golang, npm, docker]", "text-cyan-400").
		AddText("   # sandbox-runtime presets", "text-gray-500").NewLine().
		AddText("  env:", "text-cyan-400").
		AddText("                              # set env vars in container", "text-gray-500").NewLine().
		AddText("    MY_VAR: value", "text-cyan-400").NewLine().
		AddText("  envPassthrough: [MY_TOKEN]", "text-cyan-400").
		AddText("       # forward host env vars", "text-gray-500").NewLine().
		AddText("  tokens:", "text-cyan-400").
		AddText("                          # acquire cloud tokens", "text-gray-500").NewLine().
		AddText("    aws:", "text-cyan-400").
		AddText("                          # { profile, assumeRole, region }", "text-gray-500").NewLine().
		AddText("    gcp:", "text-cyan-400").
		AddText("                          # { project, credentials }", "text-gray-500").NewLine().
		AddText("    azure:", "text-cyan-400").
		AddText("                        # { clientID, clientSecret, tenantID }", "text-gray-500").NewLine().
		AddText("    github: {}", "text-cyan-400").
		AddText("                    # forward GH_TOKEN / gh CLI token", "text-gray-500").NewLine().
		AddText("    kubernetes:", "text-cyan-400").
		AddText("                   # { context }", "text-gray-500").NewLine().
		AddText("  volumes:", "text-cyan-400").
		AddText("                         # extra volume mounts", "text-gray-500").NewLine().
		AddText("    - source: /host/path", "text-cyan-400").NewLine().
		AddText("      target: /container/path", "text-cyan-400").NewLine().
		AddText("      readOnly: true", "text-cyan-400").NewLine().
		AddText("  user: { username, uid, gid }", "text-cyan-400").
		AddText("    # container user", "text-gray-500").NewLine().
		AddText("  components: [agents/design]", "text-cyan-400").
		AddText("     # selected components", "text-gray-500").NewLine().NewLine().
		AddText("See also:", "font-bold text-blue-400").NewLine().
		AddText("  captain sandbox presets", "text-green-400").
		AddText("  — list available presets with details", "text-gray-500").NewLine().
		AddText("  captain container list", "text-green-400").
		AddText("    — show discovered components", "text-gray-500").NewLine()
	return text
}

func resolvePresetVersions(presetNames []string, base string) (string, map[string]string) {
	pwd, _ := os.Getwd()
	versions := container.DetectVersions(pwd)
	if base == "claude-env:base" {
		if presetBase := presets.GetBaseImage(presetNames, versions); presetBase != "" {
			base = presetBase
		}
	}
	return base, versions
}

func discoverDefaultSelected() []container.Component {
	cfg := container.DefaultDiscoverConfig()
	components := container.DiscoverAll(cfg)
	for i := range components {
		if components[i].Category != container.CategoryProjects {
			components[i].Selected = true
		}
	}
	container.ApplyDefaults(components, cfg.LocalDepPaths)
	return components
}
