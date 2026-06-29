package api

import "fmt"

// Permissions governs what an agent may do: the base posture, named presets,
// per-tool policy, MCP servers, and plugin directories. Consolidates the legacy
// ai.Request.{PermissionMode,AllowedTools,DisallowedTools,Edit,Bare,NoMCP,SkillDirs}.
type Permissions struct {
	// Mode is the base permission posture. (ai.Request.PermissionMode)
	Mode PermissionMode `json:"mode,omitempty" yaml:"mode,omitempty" pretty:"label=Mode"`
	// Presets are named safety bundles applied before per-tool rules. (--edit/--bare)
	Presets []Preset `json:"presets,omitempty" yaml:"presets,omitempty" pretty:"label=Presets"`
	// Tools is the per-tool allow/deny/mode policy.
	Tools Tools `json:"tools,omitempty" yaml:"tools,omitempty"`
	// MCP controls Model-Context-Protocol servers.
	MCP MCP `json:"mcp,omitempty" yaml:"mcp,omitempty"`
	// Plugins are extra plugin directories (claude --plugin-dir).
	Plugins []string `json:"plugins,omitempty" yaml:"plugins,omitempty" pretty:"label=Plugins"`
}

// Tools is the per-tool policy. Allow/Deny are explicit lists (ai.Request.
// AllowedTools / DisallowedTools); Modes maps a tool to enabled|ask|disabled.
type Tools struct {
	Allow []string            `json:"allow,omitempty" yaml:"allow,omitempty" pretty:"label=Allow"`
	Deny  []string            `json:"deny,omitempty" yaml:"deny,omitempty" pretty:"label=Deny"`
	Modes map[string]ToolMode `json:"modes,omitempty" yaml:"modes,omitempty" pretty:"label=Modes"`
}

// MCP controls Model-Context-Protocol servers.
type MCP struct {
	// Disabled turns off all MCP servers. (ai.Request.NoMCP)
	Disabled bool `json:"disabled,omitempty" yaml:"disabled,omitempty" pretty:"label=Disabled"`
	// Servers is an optional allowlist subset of configured servers.
	Servers []string `json:"servers,omitempty" yaml:"servers,omitempty" pretty:"label=Servers"`
}

// Validate checks the mode, presets, and tool modes are recognised.
func (p Permissions) Validate() error {
	if !p.Mode.Valid() {
		return fmt.Errorf("invalid permission mode %q", p.Mode)
	}
	for _, preset := range p.Presets {
		if !preset.Valid() {
			return fmt.Errorf("invalid preset %q (valid: edit, bare)", preset)
		}
	}
	for tool, mode := range p.Tools.Modes {
		if !mode.Valid() {
			return fmt.Errorf("invalid tool mode %q for tool %q (valid: enabled, ask, disabled)", mode, tool)
		}
	}
	return nil
}
