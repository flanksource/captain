package api

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/flanksource/captain/pkg/api/registry"
	"gopkg.in/yaml.v3"
)

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
	Plugins ResourcePolicies `json:"plugins,omitempty" yaml:"plugins,omitempty" pretty:"label=Plugins"`
	// Skills are skill directories enabled for this request.
	Skills ResourcePolicies `json:"skills,omitempty" yaml:"skills,omitempty" pretty:"label=Skills"`
}

// Tools is the per-tool policy. Allow/Deny/Modes are retained for legacy callers;
// JSON/YAML marshals as map[tool]auto|ask|allow|deny.
type Tools struct {
	Allow []string            `json:"-" yaml:"-" pretty:"label=Allow"`
	Deny  []string            `json:"-" yaml:"-" pretty:"label=Deny"`
	Modes map[string]ToolMode `json:"-" yaml:"-" pretty:"label=Modes"`
}

// MCP controls Model-Context-Protocol servers.
type MCP struct {
	// Disabled turns off all MCP servers. (ai.Request.NoMCP)
	Disabled bool `json:"-" yaml:"-" pretty:"label=Disabled"`
	// Servers is an optional allowlist subset of configured servers.
	Servers []string `json:"-" yaml:"-" pretty:"label=Servers"`
	// Modes optionally enables/disables named configured servers.
	Modes ResourcePolicies `json:"-" yaml:"-" pretty:"label=Modes"`
}

// ResourcePolicies maps MCP/plugin/skill IDs to enabled|disabled. It accepts a
// legacy string array on decode, treating every listed item as enabled.
type ResourcePolicies map[string]ResourceMode

// HasPreset reports whether the named preset is enabled.
func (p Permissions) HasPreset(x Preset) bool {
	return slices.Contains(p.Presets, x)
}

// AllowList and DenyList project the canonical policy map onto the two lists
// every claude transport speaks (--allowedTools / --disallowedTools). They are
// the only correct source for those flags: Policies() folds an `off` tool mode
// into a deny, so reading Tools.Deny directly lets `tools: {Bash: off}` past the
// filter and the tool runs.
func (t Tools) AllowList() []string { return t.toolsWithPolicy(ToolPolicyAllow) }

// DenyList is AllowList's counterpart; see its documentation.
func (t Tools) DenyList() []string { return t.toolsWithPolicy(ToolPolicyDeny) }

func (t Tools) toolsWithPolicy(want ToolPolicy) []string {
	var out []string
	for tool, policy := range t.Policies() {
		if policy == want {
			out = append(out, tool)
		}
	}
	sort.Strings(out)
	return out
}

// RequireToolPolicySupport refuses a run whose per-tool policy the backend
// cannot carry.
//
// A deny-list exists solely to forbid a tool, so dropping it silently inverts
// the caller's intent: the agent runs with strictly more authority than the spec
// granted, and nothing in the output says so. Only the claude transports reach a
// --disallowedTools equivalent today, so the rest fail loud here rather than
// proceeding as if the policy had been applied.
//
// Allow-lists are checked too: on a backend with no tool filter, an allowlist is
// equally unenforced. `ask` is refused everywhere — no transport has a per-tool
// prompt, so it would resolve to "allowed" on the backends that advertise tool
// policy support. `auto` constrains nothing, so it needs no backend support.
func RequireToolPolicySupport(backend Backend, permissions Permissions) error {
	if asked := permissions.Tools.toolsWithPolicy(ToolPolicyAsk); len(asked) > 0 {
		return fmt.Errorf(
			"per-tool policy \"ask\" (%s) is not enforceable on any backend: transports carry allow/deny tool lists only, so the tool would run unprompted; use allow or deny",
			strings.Join(asked, ", "))
	}
	enforced := append(permissions.Tools.AllowList(), permissions.Tools.DenyList()...)
	if len(enforced) == 0 || registry.SupportsToolPolicy(backend) {
		return nil
	}
	sort.Strings(enforced)
	return fmt.Errorf(
		"backend %s cannot enforce a per-tool policy (%s), and running without it would grant more than the spec allows; remove permissions.tools or use one of: %s",
		backend, strings.Join(enforced, ", "), backendListOf(registry.ToolPolicyBackends()))
}

func backendListOf(backends []Backend) string {
	out := make([]string, len(backends))
	for i, b := range backends {
		out[i] = string(b)
	}
	return strings.Join(out, ", ")
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
			return fmt.Errorf("invalid tool mode %q for tool %q (valid: on, ask, off, auto)", mode, tool)
		}
	}
	for tool, policy := range p.Tools.Policies() {
		if !policy.Valid() {
			return fmt.Errorf("invalid tool policy %q for tool %q (valid: auto, ask, allow, deny)", policy, tool)
		}
	}
	for name, mode := range p.MCP.Modes {
		if !mode.Valid() {
			return fmt.Errorf("invalid mcp mode %q for %q (valid: enabled, disabled)", mode, name)
		}
	}
	for name, mode := range p.Plugins {
		if !mode.Valid() {
			return fmt.Errorf("invalid plugin mode %q for %q (valid: enabled, disabled)", mode, name)
		}
	}
	for name, mode := range p.Skills {
		if !mode.Valid() {
			return fmt.Errorf("invalid skill mode %q for %q (valid: enabled, disabled)", mode, name)
		}
	}
	return nil
}

// Policies returns the canonical tool policy map.
func (t Tools) Policies() map[string]ToolPolicy {
	out := map[string]ToolPolicy{}
	for _, tool := range t.Allow {
		if tool != "" {
			out[tool] = ToolPolicyAllow
		}
	}
	for _, tool := range t.Deny {
		if tool != "" {
			out[tool] = ToolPolicyDeny
		}
	}
	for tool, mode := range t.Modes {
		if tool == "" {
			continue
		}
		switch mode {
		case ToolModeOn:
			out[tool] = ToolPolicyAuto
		case ToolModeAsk:
			out[tool] = ToolPolicyAsk
		case ToolModeOff:
			out[tool] = ToolPolicyDeny
		}
	}
	return out
}

func (t Tools) MarshalJSON() ([]byte, error) {
	policies := t.Policies()
	if len(policies) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(policies)
}

func (t *Tools) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if hasRawKey(raw, "allow") || hasRawKey(raw, "deny") || hasRawKey(raw, "modes") {
		var legacy struct {
			Allow []string            `json:"allow"`
			Deny  []string            `json:"deny"`
			Modes map[string]ToolMode `json:"modes"`
		}
		if err := json.Unmarshal(data, &legacy); err != nil {
			return err
		}
		t.Allow = compactStrings(legacy.Allow)
		t.Deny = compactStrings(legacy.Deny)
		t.Modes = compactToolModes(legacy.Modes)
		for key, value := range raw {
			if key == "allow" || key == "deny" || key == "modes" {
				continue
			}
			var policy ToolPolicy
			if err := json.Unmarshal(value, &policy); err != nil {
				return err
			}
			if err := t.applyPolicy(key, policy); err != nil {
				return err
			}
		}
		return nil
	}
	var policies map[string]ToolPolicy
	if err := json.Unmarshal(data, &policies); err != nil {
		return err
	}
	return t.setPolicies(policies)
}

func (t Tools) MarshalYAML() (any, error) {
	return t.Policies(), nil
}

func (t *Tools) UnmarshalYAML(value *yaml.Node) error {
	if mappingHas(value, "allow") || mappingHas(value, "deny") || mappingHas(value, "modes") {
		var legacy struct {
			Allow []string            `yaml:"allow"`
			Deny  []string            `yaml:"deny"`
			Modes map[string]ToolMode `yaml:"modes"`
		}
		if err := value.Decode(&legacy); err != nil {
			return err
		}
		t.Allow = compactStrings(legacy.Allow)
		t.Deny = compactStrings(legacy.Deny)
		t.Modes = compactToolModes(legacy.Modes)
		return nil
	}
	var policies map[string]ToolPolicy
	if err := value.Decode(&policies); err != nil {
		return err
	}
	return t.setPolicies(policies)
}

func (t *Tools) setPolicies(policies map[string]ToolPolicy) error {
	t.Allow = nil
	t.Deny = nil
	t.Modes = nil
	for _, key := range sortedKeys(policies) {
		if err := t.applyPolicy(key, policies[key]); err != nil {
			return err
		}
	}
	return nil
}

// applyPolicy folds one tool's policy into the canonical allow/deny/modes
// representation. An unrecognised policy is an error rather than a no-op: the
// policy map is the only place it appears, so dropping it here leaves nothing
// for Permissions.Validate to catch and the tool silently runs under the
// inherited default instead of the one that was configured.
func (t *Tools) applyPolicy(tool string, policy ToolPolicy) error {
	if tool == "" {
		return nil
	}
	switch policy {
	case ToolPolicyAllow:
		t.Allow = append(t.Allow, tool)
	case ToolPolicyDeny:
		t.Deny = append(t.Deny, tool)
	case ToolPolicyAsk:
		if t.Modes == nil {
			t.Modes = map[string]ToolMode{}
		}
		t.Modes[tool] = ToolModeAsk
	case ToolPolicyAuto:
		if t.Modes == nil {
			t.Modes = map[string]ToolMode{}
		}
		t.Modes[tool] = ToolModeOn
	default:
		return fmt.Errorf("invalid tool policy %q for tool %q (valid: auto, ask, allow, deny)", policy, tool)
	}
	return nil
}

func (m MCP) MarshalJSON() ([]byte, error) {
	return json.Marshal(m.asMap())
}

func (m *MCP) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	m.Disabled = false
	m.Servers = nil
	m.Modes = nil
	for key, value := range raw {
		switch key {
		case "disabled":
			if err := json.Unmarshal(value, &m.Disabled); err != nil {
				return err
			}
		case "servers":
			if err := json.Unmarshal(value, &m.Servers); err != nil {
				return err
			}
			m.Servers = compactStrings(m.Servers)
		default:
			var mode ResourceMode
			if err := json.Unmarshal(value, &mode); err != nil {
				return err
			}
			if m.Modes == nil {
				m.Modes = ResourcePolicies{}
			}
			m.Modes[key] = mode
		}
	}
	return nil
}

func (m MCP) MarshalYAML() (any, error) {
	return m.asMap(), nil
}

func (m *MCP) UnmarshalYAML(value *yaml.Node) error {
	m.Disabled = false
	m.Servers = nil
	m.Modes = nil
	for _, keyNode := range mappingKeys(value) {
		switch keyNode.Value {
		case "disabled":
			var disabled bool
			if err := mappingValue(value, keyNode.Value).Decode(&disabled); err != nil {
				return err
			}
			m.Disabled = disabled
		case "servers":
			var servers []string
			if err := mappingValue(value, keyNode.Value).Decode(&servers); err != nil {
				return err
			}
			m.Servers = compactStrings(servers)
		default:
			var mode ResourceMode
			if err := mappingValue(value, keyNode.Value).Decode(&mode); err != nil {
				return err
			}
			if m.Modes == nil {
				m.Modes = ResourcePolicies{}
			}
			m.Modes[keyNode.Value] = mode
		}
	}
	return nil
}

func (m MCP) asMap() map[string]any {
	out := map[string]any{}
	if m.Disabled {
		out["disabled"] = true
	}
	if len(m.Servers) > 0 {
		out["servers"] = compactStrings(m.Servers)
	}
	for _, key := range sortedKeys(m.Modes) {
		out[key] = m.Modes[key]
	}
	return out
}

// EnabledServers returns the server allowlist after applying per-server modes.
func (m MCP) EnabledServers() []string {
	seen := map[string]bool{}
	var out []string
	for _, server := range m.Servers {
		if server == "" || seen[server] || m.Modes[server] == ResourceDisabled {
			continue
		}
		seen[server] = true
		out = append(out, server)
	}
	for _, server := range sortedKeys(m.Modes) {
		if m.Modes[server] != ResourceEnabled || seen[server] {
			continue
		}
		seen[server] = true
		out = append(out, server)
	}
	return out
}

func (p ResourcePolicies) Enabled() []string {
	var out []string
	for _, key := range sortedKeys(p) {
		if p[key] == ResourceEnabled {
			out = append(out, key)
		}
	}
	return out
}

func (p ResourcePolicies) MarshalJSON() ([]byte, error) {
	if len(p) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]ResourceMode(p))
}

func (p *ResourcePolicies) UnmarshalJSON(data []byte) error {
	var list []string
	if err := json.Unmarshal(data, &list); err == nil {
		*p = ResourcePolicies{}
		for _, item := range compactStrings(list) {
			(*p)[item] = ResourceEnabled
		}
		return nil
	}
	var mapped map[string]ResourceMode
	if err := json.Unmarshal(data, &mapped); err != nil {
		return err
	}
	*p = ResourcePolicies{}
	for _, key := range sortedKeys(mapped) {
		if key != "" {
			(*p)[key] = mapped[key]
		}
	}
	return nil
}

func (p ResourcePolicies) MarshalYAML() (any, error) {
	return map[string]ResourceMode(p), nil
}

func (p *ResourcePolicies) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.SequenceNode {
		var list []string
		if err := value.Decode(&list); err != nil {
			return err
		}
		*p = ResourcePolicies{}
		for _, item := range compactStrings(list) {
			(*p)[item] = ResourceEnabled
		}
		return nil
	}
	var mapped map[string]ResourceMode
	if err := value.Decode(&mapped); err != nil {
		return err
	}
	*p = ResourcePolicies{}
	for _, key := range sortedKeys(mapped) {
		if key != "" {
			(*p)[key] = mapped[key]
		}
	}
	return nil
}

func hasRawKey(m map[string]json.RawMessage, key string) bool {
	_, ok := m[key]
	return ok
}

func compactStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, item := range in {
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		out = append(out, item)
	}
	return out
}

func compactToolModes(in map[string]ToolMode) map[string]ToolMode {
	if len(in) == 0 {
		return nil
	}
	out := map[string]ToolMode{}
	for _, key := range sortedKeys(in) {
		if key != "" {
			out[key] = in[key]
		}
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		if key != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys
}

func mappingHas(value *yaml.Node, key string) bool {
	return mappingValue(value, key) != nil
}

func mappingValue(value *yaml.Node, key string) *yaml.Node {
	if value == nil || value.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(value.Content); i += 2 {
		if value.Content[i].Value == key {
			return value.Content[i+1]
		}
	}
	return nil
}

func mappingKeys(value *yaml.Node) []*yaml.Node {
	if value == nil || value.Kind != yaml.MappingNode {
		return nil
	}
	var keys []*yaml.Node
	for i := 0; i+1 < len(value.Content); i += 2 {
		keys = append(keys, value.Content[i])
	}
	return keys
}
