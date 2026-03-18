package container

import (
	"context"
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
	Name           string            `yaml:"name,omitempty"`
	Image          string            `yaml:"image"`
	Mode           Mode              `yaml:"mode"`
	BaseImage      string            `yaml:"baseImage"`
	Volumes        []Volume          `yaml:"volumes,omitempty"`
	User           UserSpec          `yaml:"user"`
	Components     []string          `yaml:"components,omitempty"`
	Options        map[string]string `yaml:"options,omitempty"`
	Patches        []Patch           `yaml:"patches,omitempty"`
	Presets        []string          `yaml:"presets,omitempty"`
	Env            map[string]string `yaml:"env,omitempty"`
	EnvPassthrough []string          `yaml:"envPassthrough,omitempty"`
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

	if mode == ModeMount {
		home := user.ContainerHome()
		for _, c := range components {
			if !c.Selected {
				continue
			}
			if c.ContentKey != "" {
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
				continue
			}
			src := c.SourcePath
			target := c.TargetPath
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

	return cfg
}

func (cfg SandboxConfig) hasOAuth() bool {
	for _, c := range cfg.Components {
		if c == "auth/OAuth account" {
			return true
		}
	}
	return false
}

func extractOAuthToken(cfg SandboxConfig) string {
	if !cfg.hasOAuth() {
		return ""
	}
	out, err := exec.Command("security", "find-generic-password",
		"-s", "Claude Code-credentials", "-a", cfg.User.Username, "-w").Output()
	if err != nil {
		return ""
	}
	var creds struct {
		ClaudeAiOauth struct {
			AccessToken string `json:"accessToken"`
		} `json:"claudeAiOauth"`
	}
	if json.Unmarshal(out, &creds) != nil {
		return ""
	}
	return creds.ClaudeAiOauth.AccessToken
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

func RunSandbox(cfg SandboxConfig, extraArgs []string) error {
	args := []string{"run", "-it", "--rm",
		"-w", os.Getenv("PWD"),
		"-v", os.Getenv("PWD") + ":" + os.Getenv("PWD"),
	}

	if cfg.Name != "" {
		args = append(args, "--name", cfg.Name)
	}

	if envToken := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); envToken != "" {
		args = append(args, "-e", "CLAUDE_CODE_OAUTH_TOKEN="+envToken)
	} else if token := extractOAuthToken(cfg); token != "" {
		args = append(args, "-e", "CLAUDE_CODE_OAUTH_TOKEN="+token)
	} else if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		args = append(args, "-e", "ANTHROPIC_API_KEY="+key)
	}

	claudeJSON := filepath.Join(os.Getenv("HOME"), ".claude.json")
	for _, envVar := range extractMCPEnvVars(cfg, claudeJSON) {
		args = append(args, "-e", envVar)
	}

	// Apply sandbox preset env vars and cache volumes
	user := HostUser{Username: cfg.User.Username, UID: cfg.User.UID, GID: cfg.User.GID}
	sandboxEnvVars, sandboxVolumes := ResolveSandboxEnv(cfg.Presets, user.ContainerHome())
	for _, e := range sandboxEnvVars {
		args = append(args, "-e", e)
	}
	for _, v := range sandboxVolumes {
		args = append(args, "-v", fmt.Sprintf("%s:%s", v.Source, v.Target))
	}

	for _, name := range cfg.EnvPassthrough {
		if val := os.Getenv(name); val != "" {
			args = append(args, "-e", name+"="+val)
		}
	}

	for k, v := range cfg.Env {
		args = append(args, "-e", k+"="+os.ExpandEnv(v))
	}

	if cfg.Tokens != nil {
		credDir, err := os.MkdirTemp("", "captain-tokens-*")
		if err != nil {
			return fmt.Errorf("creating token dir: %w", err)
		}
		defer func() { _ = os.RemoveAll(credDir) }()

		tm := sandbox.NewTokenManager(credDir)
		results, err := tm.Acquire(context.Background(), cfg.Tokens)
		if err != nil {
			return fmt.Errorf("acquiring tokens: %w", err)
		}
		for _, r := range results {
			for k, v := range r.EnvVars {
				args = append(args, "-e", k+"="+v)
			}
			for _, path := range r.WritePaths {
				args = append(args, "-v", fmt.Sprintf("%s:%s:ro", path, path))
			}
		}
	}

	for _, v := range cfg.Volumes {
		mount := fmt.Sprintf("%s:%s", v.Source, v.Target)
		if v.ReadOnly {
			mount += ":ro"
		}
		args = append(args, "-v", mount)
	}

	args = append(args, cfg.Image)
	args = append(args, extraArgs...)

	printDockerCommand(args)

	cmd := exec.Command("docker", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func RebuildFromSandbox(cfg SandboxConfig) error {
	if err := EnsureBaseImage(cfg.BaseImage); err != nil {
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
		BaseImage:     cfg.BaseImage,
		Mode:          cfg.Mode,
		Components:    components,
		User:          user,
		Patches:       cfg.Patches,
		PresetInstall: presets.InstallSnippets(cfg.Presets),
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
	fmt.Println("  Run:")
	fmt.Printf("    captain container run\n")
	fmt.Println()
	fmt.Println("  Rebuild and run:")
	fmt.Printf("    captain container run --build\n")

	rel, err := filepath.Rel(os.Getenv("HOME"), sandboxPath)
	if err == nil && !strings.HasPrefix(rel, "..") {
		fmt.Println()
		fmt.Printf("  Or with explicit config:\n")
		fmt.Printf("    captain container run ~/%s\n", rel)
	}
}

var flagsWithValues = map[string]bool{
	"-e": true, "-v": true, "-w": true,
	"--name": true, "--network": true, "-p": true,
}

var redactPrefixes = []string{"CLAUDE_CODE_OAUTH_TOKEN=", "ANTHROPIC_API_KEY="}

func printDockerCommand(args []string) {
	var lines []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if flagsWithValues[a] && i+1 < len(args) {
			val := args[i+1]
			for _, prefix := range redactPrefixes {
				if strings.HasPrefix(val, prefix) {
					val = prefix + val[len(prefix):len(prefix)+8] + "..."
					break
				}
			}
			lines = append(lines, a+" "+val)
			i++
		} else {
			lines = append(lines, a)
		}
	}
	fmt.Printf("docker %s\n\n", strings.Join(lines, " \\\n  "))
}
