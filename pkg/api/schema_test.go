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
	for _, want := range []string{"xhigh", "Permissions", "Context", "Budget", `"maxTokens"`} {
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
