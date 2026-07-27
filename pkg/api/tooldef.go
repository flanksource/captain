package api

import (
	"context"
	"fmt"
)

// ToolPreferences selects the effective per-turn mode for a tool name or group.
// An exact tool-name entry takes precedence over its group entry.
type ToolPreferences map[string]ToolMode

// Validate rejects unknown modes before a provider request is assembled.
func (p ToolPreferences) Validate() error {
	if _, exists := p[""]; exists {
		return fmt.Errorf("tool preference key cannot be empty")
	}
	for _, key := range sortedKeys(p) {
		mode := p[key]
		if _, ok := NormalizeToolMode(mode); !ok {
			return fmt.Errorf("invalid tool preference %q for %q (valid: on, ask, off, auto)", mode, key)
		}
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
	// DefaultPermission controls exposure: off omits the tool, ask routes calls
	// through Config.CanUseTool, on auto-runs, and auto defers to runtime policy.
	DefaultPermission ToolMode
	// Annotations carries opaque caller metadata (e.g. the originating CLI
	// verb/method/path) for policies that want the raw values; providers ignore it.
	Annotations map[string]string `json:",omitempty"`
}

// NeedsApproval reports whether a call to this tool must go through
// Config.CanUseTool before running.
func (t ToolDefinition) NeedsApproval() bool {
	mode, ok := NormalizeToolMode(t.DefaultPermission)
	return ok && mode == ToolModeAsk
}
