package api

import "github.com/invopop/jsonschema"

// Schema describers for the permissions block.
//
// Reflection cannot see any of this. The enums are plain string types, so they
// reflect to a bare `{"type": "string"}` that constrains nothing and offers a
// client no values to render. Tools and MCP are worse: every field on them is
// `json:"-"` behind a hand-written marshaler, so they reflect to `{}` — a schema
// that accepts anything and describes nothing, for the two fields that decide
// what an agent is allowed to do.
//
// These use invopop's *jsonschema.Schema, not clicky's map[string]any. Both
// conventions live in this package (SandboxRef uses invopop, the cmux CodexSandbox
// options use clicky) and a type handed the wrong one is silently ignored rather
// than rejected. The rule is the consumer: anything reachable from api.Spec is
// reflected by api.Schema through invopop and must use this signature.

// enumValues lifts a typed enum slice into the `any` slice invopop wants.
func enumValues[T ~string](values []T) []any {
	out := make([]any, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}

// JSONSchema declares the permission postures. Without it the editor had to
// carry its own copy of this list — twice, in TypeScript — and the copies went
// stale: `dontAsk` reached the enum here long before either of them.
func (PermissionMode) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        enumValues(AllPermissionModes()),
		Description: "Base permission posture. Which of these a backend honours is declared per backend; see the permissions capability matrix.",
	}
}

// JSONSchema declares the per-tool policy values.
func (ToolPolicy) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        enumValues(AllToolPolicies()),
		Description: "Authority for one tool: auto inherits the posture, ask requires approval, allow auto-approves, deny forbids.",
	}
}

// JSONSchema declares the resource availability values. This is a different axis
// from ToolPolicy — whether a resource is loaded at all, rather than what the
// agent may do with it — and keeping the two enums distinct in the schema is what
// stops an editor rendering them through one control.
func (ResourceMode) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        enumValues(AllResourceModes()),
		Description: "Whether this resource is loaded for the run.",
	}
}

// JSONSchema declares the presets. It is the enum a client renders as the
// picker, and the constraint that rejects a name nothing will expand.
func (Preset) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:        "string",
		Enum:        enumValues([]Preset{PresetEdit, PresetBare}),
		Description: "Named bundle of safety defaults applied before per-tool rules.",
	}
}

// JSONSchema declares the wire form of Tools, which reflection reports as `{}`
// because Allow, Deny and Modes are all json:"-" behind MarshalJSON. The wire
// shape has always been a tool→policy map; this is the first time the schema
// says so.
func (Tools) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		Description:          "Per-tool policy, keyed by tool name. An absent tool inherits the posture.",
		AdditionalProperties: ToolPolicy("").JSONSchema(),
	}
}

// JSONSchema declares the wire form of MCP, which reflection also reports as
// `{}` for the same reason.
func (MCP) JSONSchema() *jsonschema.Schema {
	properties := jsonschema.NewProperties()
	properties.Set("disabled", &jsonschema.Schema{Type: "boolean",
		Description: "Turn off all MCP servers. Honoured on claude-cli and codex-agent; accepted and dropped elsewhere."})
	properties.Set("servers", &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "string"},
		Description: "Allowlist subset of configured servers."})
	properties.Set("modes", &jsonschema.Schema{Type: "object",
		Description:          "Enable or disable named configured servers.",
		AdditionalProperties: ResourceMode("").JSONSchema()})

	return &jsonschema.Schema{
		Type:                 "object",
		Description:          "Model-Context-Protocol server controls.",
		Properties:           properties,
		AdditionalProperties: jsonschema.FalseSchema,
	}
}

// JSONSchema declares ResourcePolicies, which decodes from either a name→mode
// map or a legacy string array. Reflection sees only the map half, so a document
// using the array form failed validation against its own schema.
func (ResourcePolicies) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Description: "Resource IDs mapped to enabled|disabled. A bare string array is accepted and means every listed item is enabled.",
		OneOf: []*jsonschema.Schema{
			{Type: "object", AdditionalProperties: ResourceMode("").JSONSchema()},
			{Type: "array", Items: &jsonschema.Schema{Type: "string"}},
		},
	}
}
