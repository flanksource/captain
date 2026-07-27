package ai

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func TestWithSchemaPrompt(t *testing.T) {
	// A raw JSON schema is appended to the prompt and the native fields cleared,
	// so the run is a plain text turn that still asks for JSON.
	req := Request{Prompt: api.Prompt{
		User:       "review the diff",
		SchemaJSON: []byte(`{"type":"object","required":["pass"]}`),
	}}
	got, schema, err := WithSchemaPrompt(req)
	if err != nil {
		t.Fatalf("WithSchemaPrompt: %v", err)
	}
	if got.Prompt.Schema != nil || got.Prompt.SchemaJSON != nil {
		t.Errorf("native schema fields must be cleared, got Schema=%v SchemaJSON=%s", got.Prompt.Schema, got.Prompt.SchemaJSON)
	}
	if string(schema) != string(req.Prompt.SchemaJSON) {
		t.Errorf("preserved schema = %s, want %s", schema, req.Prompt.SchemaJSON)
	}
	if !strings.Contains(got.Prompt.User, "review the diff") {
		t.Errorf("original prompt lost: %q", got.Prompt.User)
	}
	if !strings.Contains(got.Prompt.User, `"required":["pass"]`) {
		t.Errorf("schema not appended to prompt: %q", got.Prompt.User)
	}

	// A text-mode request is returned unchanged.
	plain := Request{Prompt: api.Prompt{User: "hi"}}
	got, schema, err = WithSchemaPrompt(plain)
	if err != nil {
		t.Fatalf("WithSchemaPrompt(text): %v", err)
	}
	if got.Prompt.User != "hi" {
		t.Errorf("text prompt altered: %q", got.Prompt.User)
	}
	if len(schema) != 0 {
		t.Errorf("text prompt preserved unexpected schema: %s", schema)
	}
}

func TestValidatedStructuredData(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","required":["pass"],"properties":{"pass":{"type":"boolean"}}}`)

	got, err := ValidatedStructuredData(schema, "Result:\n```json\n{\"pass\":true}\n```", nil)
	if err != nil {
		t.Fatalf("ValidatedStructuredData(valid) error = %v", err)
	}
	if string(got) != `{"pass":true}` {
		t.Fatalf("ValidatedStructuredData(valid) = %s", got)
	}

	if _, err := ValidatedStructuredData(schema, `{"pass":"yes"}`, nil); !errors.Is(err, ErrSchemaValidation) {
		t.Fatalf("ValidatedStructuredData(invalid) error = %v, want ErrSchemaValidation", err)
	}

	outcome := &TerminalOutcome{Kind: TerminalOutcomePlan, Plan: &TerminalPlan{Content: "1. Inspect"}}
	got, err = ValidatedStructuredData(schema, "not a schema envelope", outcome)
	if err != nil || got != nil {
		t.Fatalf("native terminal outcome must bypass schema extraction, got (%s, %v)", got, err)
	}
}
