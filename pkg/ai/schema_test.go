package ai

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

type testStruct struct {
	Name     string   `json:"name"`
	Age      int      `json:"age"`
	Tags     []string `json:"tags,omitempty"`
	Optional string   `json:"optional,omitempty"`
}

func TestSchemaJSONFor(t *testing.T) {
	// A verbatim SchemaJSON is returned untouched, preserving vocabulary that the
	// reflected JSONSchema struct cannot express (minItems, maxLength).
	raw := json.RawMessage(`{"type":"array","minItems":2,"maxItems":2}`)
	got, err := SchemaJSONFor(api.Prompt{SchemaJSON: raw})
	if err != nil {
		t.Fatalf("SchemaJSONFor(SchemaJSON): %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("SchemaJSON not returned verbatim: got %s", got)
	}

	// A Go struct is reflected into an object schema.
	got, err = SchemaJSONFor(api.Prompt{Schema: testStruct{}})
	if err != nil {
		t.Fatalf("SchemaJSONFor(Schema): %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("reflected schema is not valid JSON: %v", err)
	}
	if decoded["type"] != "object" {
		t.Errorf("reflected schema type = %v, want object", decoded["type"])
	}

	// A text-mode prompt yields no schema.
	if got, err := SchemaJSONFor(api.Prompt{User: "hi"}); err != nil || got != nil {
		t.Errorf("SchemaJSONFor(text) = (%s, %v), want (nil, nil)", got, err)
	}
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

func TestBindStructuredOutput(t *testing.T) {
	type answer struct {
		Answer string `json:"answer"`
	}

	t.Run("valid JSON binds into the target", func(t *testing.T) {
		var out answer
		if err := BindStructuredOutput(&out, json.RawMessage(`{"answer":"42"}`)); err != nil {
			t.Fatalf("BindStructuredOutput: %v", err)
		}
		if out.Answer != "42" {
			t.Errorf("Answer = %q, want %q", out.Answer, "42")
		}
	})

	t.Run("empty payload is a schema-validation error", func(t *testing.T) {
		var out answer
		err := BindStructuredOutput(&out, nil)
		if !errors.Is(err, ErrSchemaValidation) {
			t.Errorf("err = %v, want ErrSchemaValidation", err)
		}
	})

	t.Run("malformed JSON is a schema-validation error", func(t *testing.T) {
		var out answer
		err := BindStructuredOutput(&out, json.RawMessage(`{not json`))
		if !errors.Is(err, ErrSchemaValidation) {
			t.Errorf("err = %v, want ErrSchemaValidation", err)
		}
	})
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
