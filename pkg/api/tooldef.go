package api

import "context"

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
	// Handler executes the tool in-process. Required.
	Handler ToolHandler `json:"-"`
	// DefaultPermission gates execution: ToolModeAsk routes the call through
	// Config.CanUseTool before the handler runs; any other value auto-runs.
	DefaultPermission ToolMode
	// Annotations carries opaque caller metadata (e.g. the originating CLI
	// verb/method/path) for policies that want the raw values; providers ignore it.
	Annotations map[string]string `json:",omitempty"`
}

// NeedsApproval reports whether a call to this tool must go through
// Config.CanUseTool before running.
func (t ToolDefinition) NeedsApproval() bool { return t.DefaultPermission == ToolModeAsk }
