// Permissions codec: the wire shapes for the policy maps under Permissions.
//
// Each of Tools, MCP and ResourcePolicies accepts more than one encoding — the
// current map form and the legacy object or string-array form it replaced — so
// its (Un)MarshalJSON/YAML pairs are substantially longer than the type they
// belong to. They live here, split out of permissions.go, so that file stays
// about what a permission means rather than how it is spelled on disk.
package api

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func (t Tools) MarshalJSON() ([]byte, error) {
	if len(t) == 0 {
		return []byte("{}"), nil
	}
	return json.Marshal(map[string]ToolPolicy(t))
}

func (t *Tools) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	if hasRawKey(raw, "allow") || hasRawKey(raw, "deny") || hasRawKey(raw, "modes") {
		var legacy struct {
			Allow []string          `json:"allow"`
			Deny  []string          `json:"deny"`
			Modes map[string]string `json:"modes"`
		}
		if err := json.Unmarshal(data, &legacy); err != nil {
			return err
		}
		if err := t.setLegacy(legacy.Allow, legacy.Deny, legacy.Modes); err != nil {
			return err
		}
		for _, key := range sortedKeys(raw) {
			if key == "allow" || key == "deny" || key == "modes" {
				continue
			}
			var policy string
			if err := json.Unmarshal(raw[key], &policy); err != nil {
				return err
			}
			if err := t.set(key, policy, ParseToolPolicyOptions{}); err != nil {
				return err
			}
		}
		return nil
	}
	var policies map[string]string
	if err := json.Unmarshal(data, &policies); err != nil {
		return err
	}
	return t.setPolicies(policies)
}

func (t Tools) MarshalYAML() (any, error) {
	return map[string]ToolPolicy(t), nil
}

func (t *Tools) UnmarshalYAML(value *yaml.Node) error {
	if mappingHas(value, "allow") || mappingHas(value, "deny") || mappingHas(value, "modes") {
		var legacy struct {
			Allow []string          `yaml:"allow"`
			Deny  []string          `yaml:"deny"`
			Modes map[string]string `yaml:"modes"`
		}
		if err := value.Decode(&legacy); err != nil {
			return err
		}
		return t.setLegacy(legacy.Allow, legacy.Deny, legacy.Modes)
	}
	var policies map[string]string
	if err := value.Decode(&policies); err != nil {
		return err
	}
	return t.setPolicies(policies)
}

// setLegacy folds the legacy {allow, deny, modes} object into the policy map.
//
// modes is parsed with LegacyOn: auto rather than allow, because this encoding
// carries allow in its own Allow list — leaving "on" to mean "enabled, defer
// gating". spec.toolPreferences has no such list and so reads "on" as allow.
// Both preserve what the respective configs meant before the vocabularies were
// unified; see ParseToolPolicyOptions.LegacyOn.
func (t *Tools) setLegacy(allow, deny []string, modes map[string]string) error {
	*t = nil
	for _, tool := range compactStrings(allow) {
		t.put(tool, ToolPolicyAllow)
	}
	for _, tool := range compactStrings(deny) {
		t.put(tool, ToolPolicyDeny)
	}
	for _, tool := range sortedKeys(modes) {
		if err := t.set(tool, modes[tool], ParseToolPolicyOptions{LegacyOn: ToolPolicyAuto}); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tools) setPolicies(policies map[string]string) error {
	*t = nil
	for _, tool := range sortedKeys(policies) {
		if err := t.set(tool, policies[tool], ParseToolPolicyOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// set parses one tool's policy into the map. An unrecognised value is an error
// rather than a no-op: the policy map is the only place it appears, so dropping
// it here leaves nothing for Permissions.Validate to catch and the tool silently
// runs under the inherited default instead of the one that was configured.
func (t *Tools) set(tool, value string, opts ParseToolPolicyOptions) error {
	if strings.TrimSpace(tool) == "" {
		return nil
	}
	policy, ok := ParseToolPolicy(value, opts)
	if !ok {
		return fmt.Errorf("invalid tool policy %q for tool %q (valid: auto, ask, allow, deny)", value, tool)
	}
	t.put(tool, policy)
	return nil
}

func (t *Tools) put(tool string, policy ToolPolicy) {
	if *t == nil {
		*t = Tools{}
	}
	(*t)[tool] = policy
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
