package ai

import (
	"encoding/json"
	"errors"
	"strings"
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

func TestSchemaJSONForBackend_AnthropicSanitizesUnsupportedConstraints(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"properties":{
			"title":{"type":"string","minLength":3,"maxLength":40,"format":"slug"},
			"items":{"type":"array","minItems":1,"maxItems":2,"items":{"type":"object","properties":{"name":{"type":"string","pattern":"^[a-z]+$"}}}}
		},
		"required":["title"]
	}`)
	got, err := SchemaJSONForBackend(api.BackendAnthropic, api.Prompt{SchemaJSON: raw})
	if err != nil {
		t.Fatalf("SchemaJSONForBackend: %v", err)
	}
	if string(raw) == string(got) {
		t.Fatal("anthropic schema should be transformed, got original")
	}
	if !json.Valid(raw) || !containsJSONKey(t, raw, "maxItems") {
		t.Fatalf("original schema should remain unchanged: %s", raw)
	}

	var schema map[string]any
	if err := json.Unmarshal(got, &schema); err != nil {
		t.Fatalf("transformed schema invalid JSON: %v", err)
	}
	if schema["additionalProperties"] != false {
		t.Fatalf("root additionalProperties = %v, want false", schema["additionalProperties"])
	}
	title := schema["properties"].(map[string]any)["title"].(map[string]any)
	for _, key := range []string{"minLength", "maxLength", "format"} {
		if _, ok := title[key]; ok {
			t.Fatalf("title still has unsupported %s: %#v", key, title)
		}
	}
	if desc := title["description"].(string); !strings.Contains(desc, "minLength=3") || !strings.Contains(desc, "format=slug") {
		t.Fatalf("title description = %q, want removed constraints", desc)
	}
	items := schema["properties"].(map[string]any)["items"].(map[string]any)
	if _, ok := items["maxItems"]; ok {
		t.Fatalf("items still has maxItems: %#v", items)
	}
	nested := items["items"].(map[string]any)
	if nested["additionalProperties"] != false {
		t.Fatalf("nested additionalProperties = %v, want false", nested["additionalProperties"])
	}
	name := nested["properties"].(map[string]any)["name"].(map[string]any)
	if _, ok := name["pattern"]; ok {
		t.Fatalf("name still has pattern: %#v", name)
	}
}

func TestSchemaJSONForBackend_NonAnthropicKeepsOriginalSchema(t *testing.T) {
	raw := json.RawMessage(`{"type":"array","maxItems":2}`)
	got, err := SchemaJSONForBackend(api.BackendOpenAI, api.Prompt{SchemaJSON: raw})
	if err != nil {
		t.Fatalf("SchemaJSONForBackend: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("non-anthropic schema changed: got %s want %s", got, raw)
	}
}

func containsJSONKey(t *testing.T, raw json.RawMessage, key string) bool {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var walk func(any) bool
	walk = func(node any) bool {
		switch n := node.(type) {
		case map[string]any:
			if _, ok := n[key]; ok {
				return true
			}
			for _, child := range n {
				if walk(child) {
					return true
				}
			}
		case []any:
			for _, child := range n {
				if walk(child) {
					return true
				}
			}
		}
		return false
	}
	return walk(v)
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
