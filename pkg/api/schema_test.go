package api

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSchemaJSON checks the generated JSON schema is valid JSON, enumerates the
// effort tiers (including the new "xhigh"), and references the nested types.
func TestSchemaJSON(t *testing.T) {
	data, err := SchemaJSON(&Spec{})
	if err != nil {
		t.Fatalf("SchemaJSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	s := string(data)
	for _, want := range []string{"xhigh", "max", "ultra", "Permissions", "Setup", "Budget", `"maxTokens"`} {
		if !strings.Contains(s, want) {
			t.Errorf("schema missing %q\n%s", want, s)
		}
	}
	if _, ok := doc["$defs"]; !ok {
		t.Errorf("schema should reference nested types under $defs\n%s", s)
	}
}

func TestSchemaJSON_PromptRequired(t *testing.T) {
	data, _ := SchemaJSON(&Prompt{})
	if !strings.Contains(string(data), `"required"`) || !strings.Contains(string(data), `"user"`) {
		t.Errorf("Prompt schema should mark user required:\n%s", data)
	}
}

// TestSchemaJSON_FallbacksSelfReference pins that the self-referential
// Model.Fallbacks ([]Model) reflects into a bounded schema via a $ref back to the
// Model definition (rather than recursing forever) and still exposes the field.
func TestSchemaJSON_FallbacksSelfReference(t *testing.T) {
	data, err := SchemaJSON(&Spec{})
	if err != nil {
		t.Fatalf("SchemaJSON: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "fallbacks") {
		t.Errorf("schema missing fallbacks field:\n%s", s)
	}
	if !strings.Contains(s, "#/$defs/Model") {
		t.Errorf("Fallbacks should reference the Model definition ($ref), got:\n%s", s)
	}
}
