// ABOUTME: YAML fixture schema for benchmarking Claude configurations.
// ABOUTME: Load reads a fixture file; Merge layers Defaults under each Run.

package fixture

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

type Fixture struct {
	Name        string `yaml:"name,omitempty"`
	Description string `yaml:"description,omitempty"`
	Prompt      string `yaml:"prompt,omitempty"`
	System      string `yaml:"system,omitempty"`
	CWD         string `yaml:"cwd,omitempty"`
	Baseline    string `yaml:"baseline,omitempty"`
	Repeat      int    `yaml:"repeat,omitempty"`
	Defaults    Run    `yaml:"defaults,omitempty"`
	Runs        []Run  `yaml:"runs"`

	Dir string `yaml:"-"`
}

type Run struct {
	Name                 string            `yaml:"name,omitempty"`
	Prompt               string            `yaml:"prompt,omitempty"`
	System               string            `yaml:"system,omitempty"`
	Model                string            `yaml:"model,omitempty"`
	Timeout              string            `yaml:"timeout,omitempty"`
	CWD                  string            `yaml:"cwd,omitempty"`
	PermissionMode       string            `yaml:"permissionMode,omitempty"`
	AppendSystemPrompt   string            `yaml:"appendSystemPrompt,omitempty"`
	Settings             string            `yaml:"settings,omitempty"`
	MaxBudgetUSD         string            `yaml:"maxBudgetUSD,omitempty"`
	Repeat               int               `yaml:"repeat,omitempty"`
	Tools                []string          `yaml:"tools,omitempty"`
	AllowedTools         []string          `yaml:"allowedTools,omitempty"`
	DisallowedTools      []string          `yaml:"disallowedTools,omitempty"`
	MCPConfig            []string          `yaml:"mcpConfig,omitempty"`
	AddDir               []string          `yaml:"addDir,omitempty"`
	Betas                []string          `yaml:"betas,omitempty"`
	ExtraArgs            []string          `yaml:"extraArgs,omitempty"`
	Env                  map[string]string `yaml:"env,omitempty"`
	PromptCaching        *bool             `yaml:"promptCaching,omitempty"`
	StrictMCPConfig      *bool             `yaml:"strictMCPConfig,omitempty"`
	NoSessionPersistence *bool             `yaml:"noSessionPersistence,omitempty"`
	Bare                 *bool             `yaml:"bare,omitempty"`
}

func Load(path string) (*Fixture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f Fixture
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing fixture: %w", err)
	}
	if len(f.Runs) == 0 {
		return nil, fmt.Errorf("fixture must contain at least one run")
	}
	f.Dir = filepath.Dir(path)
	return &f, nil
}

func (f *Fixture) Merge(run Run) Run {
	merged := f.Defaults
	if run.Name != "" {
		merged.Name = run.Name
	}
	if run.Prompt != "" {
		merged.Prompt = run.Prompt
	}
	if run.System != "" {
		merged.System = run.System
	}
	if run.Model != "" {
		merged.Model = run.Model
	}
	if run.Timeout != "" {
		merged.Timeout = run.Timeout
	}
	if run.CWD != "" {
		merged.CWD = run.CWD
	}
	if run.PermissionMode != "" {
		merged.PermissionMode = run.PermissionMode
	}
	if run.AppendSystemPrompt != "" {
		merged.AppendSystemPrompt = run.AppendSystemPrompt
	}
	if run.Settings != "" {
		merged.Settings = run.Settings
	}
	if run.MaxBudgetUSD != "" {
		merged.MaxBudgetUSD = run.MaxBudgetUSD
	}
	if run.Repeat != 0 {
		merged.Repeat = run.Repeat
	}
	if run.Tools != nil {
		merged.Tools = run.Tools
	}
	if run.AllowedTools != nil {
		merged.AllowedTools = run.AllowedTools
	}
	if run.DisallowedTools != nil {
		merged.DisallowedTools = run.DisallowedTools
	}
	if run.MCPConfig != nil {
		merged.MCPConfig = run.MCPConfig
	}
	if run.AddDir != nil {
		merged.AddDir = run.AddDir
	}
	if run.Betas != nil {
		merged.Betas = run.Betas
	}
	if run.ExtraArgs != nil {
		merged.ExtraArgs = run.ExtraArgs
	}
	if run.Env != nil {
		merged.Env = run.Env
	}
	if run.PromptCaching != nil {
		merged.PromptCaching = run.PromptCaching
	}
	if run.StrictMCPConfig != nil {
		merged.StrictMCPConfig = run.StrictMCPConfig
	}
	if run.NoSessionPersistence != nil {
		merged.NoSessionPersistence = run.NoSessionPersistence
	}
	if run.Bare != nil {
		merged.Bare = run.Bare
	}

	if merged.Prompt == "" {
		merged.Prompt = f.Prompt
	}
	if merged.System == "" {
		merged.System = f.System
	}
	if merged.CWD == "" {
		merged.CWD = f.CWD
	}
	return merged
}
