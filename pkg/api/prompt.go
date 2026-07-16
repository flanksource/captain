package api

import (
	"encoding/json"
	"fmt"
	"strings"
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
	// SchemaStrictness governs how a response that fails JSON-schema validation is
	// handled: "" uses the backend default (Anthropic structured output retries,
	// others skip post-validation); "none" always skips; "warning" logs and
	// continues; "error" fails; "retry" re-asks the model with the validation
	// error, then fails. Only meaningful alongside a schema (Schema or SchemaJSON).
	SchemaStrictness SchemaStrictness `json:"schemaStrictness,omitempty" yaml:"schemaStrictness,omitempty" pretty:"-"`
	// Metadata is arbitrary caller metadata. (ai.Request.Metadata)
	Metadata map[string]string `json:"metadata,omitempty" yaml:"metadata,omitempty" pretty:"label=Metadata"`
	// Attachments are ordered multimodal inputs sent with the user prompt.
	Attachments []AttachmentRef `json:"attachments,omitempty" yaml:"attachments,omitempty" pretty:"label=Attachments"`
}

func (p Prompt) CacheIdentity() string {
	var identity strings.Builder
	identity.WriteString(p.User)
	for _, attachment := range p.Attachments {
		identity.WriteByte('\n')
		identity.WriteString(attachment.ID)
		identity.WriteByte('|')
		identity.WriteString(attachment.MediaType)
		identity.WriteByte('|')
		identity.WriteString(attachment.SHA256)
	}
	return identity.String()
}

// HasSchema reports whether the prompt requests structured output by either
// mechanism (a reflected Go struct or a pre-built JSON schema).
func (p Prompt) HasSchema() bool { return p.Schema != nil || len(p.SchemaJSON) > 0 }

// Validate requires prompt text or at least one attachment.
func (p Prompt) Validate() error {
	if p.User == "" && len(p.Attachments) == 0 {
		return fmt.Errorf("prompt text is required when no attachment is supplied")
	}
	for i, attachment := range p.Attachments {
		if err := attachment.Validate(); err != nil {
			return fmt.Errorf("attachment %d: %w", i+1, err)
		}
	}
	if err := p.SchemaStrictness.Validate(); err != nil {
		return err
	}
	return nil
}
