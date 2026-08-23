package api

import (
	"context"
	"encoding/json"
	"fmt"

	clickyentity "github.com/flanksource/clicky/entity"
	"gopkg.in/yaml.v3"
)

// ToolPreferences selects the effective per-turn policy for a tool name or
// group. An exact tool-name entry takes precedence over its group entry.
//
// This encoding has no separate allow list, so the legacy "on" spelling is the
// only way it could say auto-run and decodes to allow — unlike the legacy
// permissions.tools modes map, where an Allow list already carries allow and
// "on" means auto. See ParseToolPolicyOptions.LegacyOn.
type ToolPreferences map[string]ToolPolicy

const legacyPreferenceOn = ToolPolicyAllow

// Validate rejects unknown policies before a provider request is assembled.
func (p ToolPreferences) Validate() error {
	if _, exists := p[""]; exists {
		return fmt.Errorf("tool preference key cannot be empty")
	}
	for _, key := range sortedKeys(p) {
		if policy := p[key]; !policy.Valid() {
			return fmt.Errorf("invalid tool preference %q for %q (valid: auto, ask, allow, deny)", policy, key)
		}
	}
	return nil
}

func (p *ToolPreferences) UnmarshalJSON(data []byte) error {
	var raw map[string]string
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	return p.setAll(raw)
}

func (p *ToolPreferences) UnmarshalYAML(value *yaml.Node) error {
	var raw map[string]string
	if err := value.Decode(&raw); err != nil {
		return err
	}
	return p.setAll(raw)
}

func (p *ToolPreferences) setAll(raw map[string]string) error {
	*p = nil
	for _, key := range sortedKeys(raw) {
		policy, ok := ParseToolPolicy(raw[key], ParseToolPolicyOptions{LegacyOn: legacyPreferenceOn})
		if !ok {
			return fmt.Errorf("invalid tool preference %q for %q (valid: auto, ask, allow, deny)", raw[key], key)
		}
		if *p == nil {
			*p = ToolPreferences{}
		}
		(*p)[key] = policy
	}
	return nil
}

// ToolHandler runs a caller-supplied tool. input is the model's tool-call
// arguments (the decoded JSON object); the returned value is marshaled back to
// the model as the tool result. Returning an error surfaces it to the model as
// a failed tool call.
type ToolHandler func(ctx context.Context, input map[string]any) (any, error)

// ToolDefinition is a caller-supplied tool that a tool-capable provider (see
// ToolCapableProvider) exposes to the model and executes in-process. It carries
// a Go handler, so — like CanUseTool — it is a runtime concern that lives on
// Config, never on the serializable Spec, and is never marshaled.
type ToolDefinition struct {
	// Name is the tool id the model calls (provider-safe: letters, digits, _-).
	Name string
	// Description tells the model when/how to use the tool.
	Description string
	// InputSchema is the JSON Schema (decoded to a map) for the tool arguments.
	// Nil means a no-argument tool.
	InputSchema map[string]any
	// Group is the preference key shared by related tools. A tool-name preference
	// overrides a group preference.
	Group string
	// Parent and Icon retain presentation metadata for catalogs without affecting
	// provider execution.
	Parent string
	Icon   string
	// Strict opts this tool into provider strict-schema enforcement. Safety hints
	// prioritize tools when a provider caps strict-tool definitions.
	Strict          *bool
	ReadOnlyHint    *bool
	DestructiveHint *bool
	IdempotentHint  *bool
	// Handler executes the tool in-process. Required.
	Handler ToolHandler `json:"-"`
	// DefaultPermission controls exposure: deny omits the tool, ask routes calls
	// through Config.CanUseTool, allow auto-runs, and auto defers to runtime policy.
	DefaultPermission ToolPolicy
	// Operation is the clicky RPC operation this tool projects, when it projects
	// one. It carries the originating entity, verb, scope, method and path as the
	// typed model clicky already has, so a permission rule can select on them
	// without either side maintaining a string copy. Nil for tools that are not
	// clicky operations; providers ignore it.
	Operation *clickyentity.RPCOperation `json:"-"`
	// Annotations carries opaque caller metadata for policies that want raw
	// values; providers ignore it. A clicky operation's own facts travel in
	// Operation, not here.
	Annotations map[string]string `json:",omitempty"`
}

// NeedsApproval reports whether a call to this tool must go through
// Config.CanUseTool before running.
func (t ToolDefinition) NeedsApproval() bool {
	policy, ok := NormalizeToolPolicy(string(t.DefaultPermission))
	return ok && policy == ToolPolicyAsk
}
