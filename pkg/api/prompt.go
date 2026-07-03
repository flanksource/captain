package api

import (
	"encoding/json"
	"fmt"
)

// Prompt is the instruction payload: the user prompt plus optional system
// framing, a structured-output schema target, and diagnostic metadata.
// Consolidates the legacy ai.Request.{Prompt,SystemPrompt,AppendSystemPrompt,
// Source,StructuredOutput,Metadata}.
type Prompt struct {
	// User is the user prompt. (ai.Request.Prompt)
	User string `json:"user" yaml:"user" jsonschema:"required" pretty:"label=Prompt"`
	// System is the system prompt. (ai.Request.SystemPrompt)
	System string `json:"system,omitempty" yaml:"system,omitempty" pretty:"label=System"`
	// AppendSystem is appended to the default system prompt. (ai.Request.AppendSystemPrompt)
	AppendSystem string `json:"appendSystem,omitempty" yaml:"appendSystem,omitempty" pretty:"label=Append System"`
	// Source identifies the prompt's origin (e.g. a .prompt filename) for diagnostics.
	Source string `json:"source,omitempty" yaml:"source,omitempty" pretty:"label=Source"`
	// Schema is the Go struct the response must conform to (structured output); a
	// runtime-only Go type, never serialized as data. (ai.Request.StructuredOutput)
	Schema any `json:"-" yaml:"-" pretty:"-"`
	// SchemaJSON is a pre-built JSON Schema (e.g. from a .prompt frontmatter
	// output block or a caller-generated schema) that the response must conform
	// to. Unlike Schema it is sent to the model verbatim — never reflected from a
	// Go type and never round-tripped through JSONSchema — so it preserves the
	// full JSON Schema vocabulary (minItems, maxLength, …). When set, the raw JSON
	// reply is also left on Response.Text for tolerant callers. Schema and
	// SchemaJSON are mutually exclusive.
	SchemaJSON json.RawMessage `json:"schemaJSON,omitempty" yaml:"schemaJSON,omitempty" pretty:"-"`
	// Metadata is arbitrary caller metadata. (ai.Request.Metadata)
	Metadata map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty" pretty:"label=Metadata"`
}

// HasSchema reports whether the prompt requests structured output by either
// mechanism (a reflected Go struct or a pre-built JSON schema).
func (p Prompt) HasSchema() bool { return p.Schema != nil || len(p.SchemaJSON) > 0 }

// Validate requires a non-empty user prompt.
func (p Prompt) Validate() error {
	if p.User == "" {
		return fmt.Errorf("prompt text is required")
	}
	return nil
}
