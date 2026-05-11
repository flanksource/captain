package container

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/flanksource/captain/pkg/sandbox"
	"github.com/flanksource/captain/pkg/sandbox/presets"
	"gopkg.in/yaml.v3"
)

type SandboxConfig struct {
	Name           string                `yaml:"name,omitempty"`
	Image          string                `yaml:"image"`
	Mode           Mode                  `yaml:"mode"`
	BaseImage      string                `yaml:"baseImage"`
	Volumes        []Volume              `yaml:"volumes,omitempty"`
	User           UserSpec              `yaml:"user"`
	Components     []string              `yaml:"components,omitempty"`
	Options        map[string]string     `yaml:"options,omitempty"`
	Patches        []Patch               `yaml:"patches,omitempty"`
	Presets        []string              `yaml:"presets,omitempty"`
	Env            map[string]string     `yaml:"env,omitempty"`
	EnvPassthrough []string              `yaml:"envPassthrough,omitempty"`
	Tokens         *sandbox.TokensConfig `yaml:"tokens,omitempty"`
}

type Patch struct {
	Target         string         `yaml:"target"`
	StrategicMerge map[string]any `yaml:"strategicMerge,omitempty"`
	JSONPatch      []JSONPatchOp  `yaml:"jsonPatch,omitempty"`
}

type JSONPatchOp struct {
	Op    string `yaml:"op"`
	Path  string `yaml:"path"`
	Value any    `yaml:"value,omitempty"`
}

type Volume struct {
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"readOnly,omitempty"`
}

type UserSpec struct {
	Username string `yaml:"username"`
	UID      int    `yaml:"uid"`
	GID      int    `yaml:"gid"`
}

const defaultSandboxFile = ".container-sandbox.yaml"

func SaveSandboxConfig(path string, cfg SandboxConfig) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling sandbox config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func LoadSandboxConfig(path string) (SandboxConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return SandboxConfig{}, fmt.Errorf("reading sandbox config: %w", err)
	}
	var cfg SandboxConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return SandboxConfig{}, fmt.Errorf("parsing sandbox config: %w", err)
	}
	return cfg, nil
}

func ImageTag(presets []string) string {
	if len(presets) == 0 {
		return "claude-env:custom"
	}
	return "claude-env:" + strings.Join(presets, "-")
}

func BuildSandboxConfig(mode Mode, baseImage string, components []Component, user HostUser, presets []string) SandboxConfig {
	cfg := SandboxConfig{
		Image:     ImageTag(presets),
		Mode:      mode,
		BaseImage: baseImage,
		Presets:   presets,
		User: UserSpec{
			Username: user.Username,
			UID:      user.UID,
			GID:      user.GID,
		},
	}

	options := make(map[string]string)
	for _, c := range components {
		if !c.Selected {
			continue
		}
		cfg.Components = append(cfg.Components, c.String())
		if c.OptionValue != "" {
			options[c.ContentKey] = c.OptionValue
		}
	}
	if len(options) > 0 {
		cfg.Options = options
	}

	home := user.ContainerHome()
	claudeDir := filepath.Join(os.Getenv("HOME"), ".claude")
	grouped := GroupByCategory(components)

	// Categories eligible for folder-level mounts
	mountableCategories := []Category{
		CategoryAgents, CategorySkills, CategoryCommands, CategoryHooks, CategoryMCP,
	}
	for _, cat := range mountableCategories {
		all := grouped[cat]
		if len(all) == 0 {
			continue
		}
		selected := FilterSelected(all)
		if len(selected) == 0 {
			continue
		}
		if len(selected) == len(all) {
			cfg.Volumes = append(cfg.Volumes, Volume{
				Source:   filepath.Join(claudeDir, string(cat)),
				Target:   home + "/.claude/" + string(cat),
				ReadOnly: true,
			})
		} else {
			for _, c := range selected {
				src, target := c.SourcePath, c.TargetPath
				if c.IsDir {
					src += "/"
					target += "/"
				}
				cfg.Volumes = append(cfg.Volumes, Volume{
					Source:   src,
					Target:   target,
					ReadOnly: true,
				})
			}
		}
	}

	// Mount individual file components (CLAUDE.md, plugins, etc.)
	for _, c := range components {
		if !c.Selected || c.ContentKey != "" {
			continue
		}
		switch c.Category {
		case CategoryAgents, CategorySkills, CategoryCommands, CategoryHooks, CategoryMCP:
			continue // already handled above
		}
		cfg.Volumes = append(cfg.Volumes, Volume{
			Source:   c.SourcePath,
			Target:   c.TargetPath,
			ReadOnly: true,
		})
	}

	// Project volumes (workspace + project metadata)
	for _, c := range components {
		if !c.Selected || c.ContentKey == "" {
			continue
		}
		if c.GitRoot != "" {
			cfg.Volumes = append(cfg.Volumes, Volume{
				Source: c.GitRoot,
				Target: "/workspace/" + filepath.Base(c.GitRoot),
			})
		}
		if c.ProjectPath != "" {
			cfg.Volumes = append(cfg.Volumes, Volume{
				Source:   c.ProjectPath,
				Target:   home + "/.claude/projects/" + filepath.Base(c.ProjectPath),
				ReadOnly: true,
			})
		}
	}

	return cfg
}

var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func extractMCPEnvVars(cfg SandboxConfig, claudeJSON string) []string {
	selected := make(map[string]bool)
	for _, c := range cfg.Components {
		if name, ok := strings.CutPrefix(c, "mcp-servers/"); ok {
			selected[name] = true
		}
	}
	if len(selected) == 0 {
		return nil
	}

	data, err := os.ReadFile(claudeJSON)
	if err != nil {
		return nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	serversRaw, ok := raw["mcpServers"]
	if !ok {
		return nil
	}
	var servers map[string]json.RawMessage
	if err := json.Unmarshal(serversRaw, &servers); err != nil {
		return nil
	}

	seen := make(map[string]bool)
	var result []string
	for name, serverJSON := range servers {
		if !selected[name] {
			continue
		}
		for _, match := range envVarPattern.FindAllSubmatch(serverJSON, -1) {
			varName := string(match[1])
			if seen[varName] {
				continue
			}
			seen[varName] = true
			if val := os.Getenv(varName); val != "" {
				result = append(result, varName+"="+val)
			}
		}
	}
	return result
}

func DefaultContainerName(image, workDir string) string {
	tag := strings.ReplaceAll(image, ":", "-")
	tag = strings.ReplaceAll(tag, "/", "-")
	dir := filepath.Base(workDir)
	return tag + "-" + dir
}

func IsContainerRunning(name string) bool {
	out, err := exec.Command("docker", "inspect", "--format", "{{.State.Running}}", name).Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "true"
}

func RebuildFromSandbox(cfg SandboxConfig) error {
	pwd, _ := os.Getwd()
	versions := DetectVersions(pwd)

	base := cfg.BaseImage
	if base == "claude-env:base" {
		if presetBase := presets.GetBaseImage(cfg.Presets, versions); presetBase != "" {
			base = presetBase
		}
	}

	if err := EnsureBaseImage(base); err != nil {
		return fmt.Errorf("base image: %w", err)
	}

	components := DiscoverAll(DefaultDiscoverConfig())
	ApplySelections(components, cfg.Components, cfg.Options)

	user := HostUser{
		Username: cfg.User.Username,
		UID:      cfg.User.UID,
		GID:      cfg.User.GID,
		HomeDir:  os.Getenv("HOME"),
	}

	contextDir, err := Generate(GenerateInput{
		Name:          ImageTag(cfg.Presets),
		BaseImage:     base,
		Mode:          cfg.Mode,
		Components:    components,
		User:          user,
		Patches:       cfg.Patches,
		PresetInstall: presets.ResolveInstallSnippets(cfg.Presets, versions),
	})
	if err != nil {
		return fmt.Errorf("generate: %w", err)
	}

	return Build(BuildInput{
		Tag:        ImageTag(cfg.Presets),
		ContextDir: contextDir,
		User:       user,
	})
}

func ApplySelections(components []Component, selected []string, options map[string]string) {
	selSet := make(map[string]bool, len(selected))
	for _, s := range selected {
		selSet[s] = true
	}
	for i := range components {
		components[i].Selected = selSet[components[i].String()]
		if opt, ok := options[components[i].ContentKey]; ok && components[i].Selected {
			components[i].OptionValue = opt
		}
	}
}

func SandboxConfigPath() string {
	pwd, _ := os.Getwd()
	return filepath.Join(pwd, defaultSandboxFile)
}

func PrintRunInstructions(sandboxPath string) {
	fmt.Println()
	fmt.Printf("  Sandbox config: %s\n", sandboxPath)
	fmt.Println()
	fmt.Println("  Add to PATH:")
	fmt.Println("    export PATH=\"$PWD/.container-sandbox:$PATH\"")
	fmt.Println()
	fmt.Println("  Then:")
	fmt.Println("    sbx-build    # build the image")
	fmt.Println("    sbx-start    # start container in background")
	fmt.Println("    sbx-exec     # exec into container (or sbx-exec <cmd>)")
	fmt.Println("    sbx-run      # start + exec in one step")
	fmt.Println("    sbx-stop     # stop the container")
	fmt.Println("    sbx-rm       # remove the container")
}
