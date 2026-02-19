package provider

import (
	"encoding/json"
	"testing"
)

type testStruct struct {
	Name     string   `json:"name"`
	Age      int      `json:"age"`
	Tags     []string `json:"tags,omitempty"`
	Optional string   `json:"optional,omitempty"`
}

func TestGenerateJSONSchema(t *testing.T) {
	schema, err := GenerateJSONSchema(testStruct{})
	if err != nil {
		t.Fatalf("GenerateJSONSchema: %v", err)
	}

	if schema.Type != "object" {
		t.Errorf("Type = %q, want %q", schema.Type, "object")
	}
	if len(schema.Properties) != 4 {
		t.Errorf("Properties count = %d, want 4", len(schema.Properties))
	}
	if schema.Properties["name"].Type != "string" {
		t.Errorf("name type = %q, want string", schema.Properties["name"].Type)
	}
	if schema.Properties["age"].Type != "integer" {
		t.Errorf("age type = %q, want integer", schema.Properties["age"].Type)
	}
	if schema.Properties["tags"].Type != "array" {
		t.Errorf("tags type = %q, want array", schema.Properties["tags"].Type)
	}

	// name and age are required, tags and optional have omitempty
	if len(schema.Required) != 2 {
		t.Errorf("Required count = %d, want 2", len(schema.Required))
	}
}

func TestGenerateJSONSchema_Pointer(t *testing.T) {
	schema, err := GenerateJSONSchema(&testStruct{})
	if err != nil {
		t.Fatalf("GenerateJSONSchema from pointer: %v", err)
	}
	if schema.Type != "object" {
		t.Errorf("Type = %q, want %q", schema.Type, "object")
	}
}

func TestGenerateJSONSchema_NonStruct(t *testing.T) {
	_, err := GenerateJSONSchema("string")
	if err == nil {
		t.Error("expected error for non-struct type")
	}
}

func TestSchemaToJSON(t *testing.T) {
	schema := &JSONSchema{Type: "string"}
	s, err := SchemaToJSON(schema)
	if err != nil {
		t.Fatalf("SchemaToJSON: %v", err)
	}
	var decoded JSONSchema
	if err := json.Unmarshal([]byte(s), &decoded); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if decoded.Type != "string" {
		t.Errorf("Type = %q, want string", decoded.Type)
	}
}
