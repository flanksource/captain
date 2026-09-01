package ai

import (
	"encoding/json"
	"errors"
	"reflect"
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

func TestSchemaJSONForRuntime_AnthropicSanitizesUnsupportedConstraints(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"properties":{
			"title":{"type":"string","minLength":3,"maxLength":40,"format":"slug"},
			"items":{"type":"array","minItems":1,"maxItems":2,"items":{"type":"object","properties":{"name":{"type":"string","pattern":"^[a-z]+$"}}}}
		},
		"required":["title"]
	}`)
	got, err := SchemaJSONForRuntime(api.Anthropic, api.ModeAPI, api.Prompt{SchemaJSON: raw})
	if err != nil {
		t.Fatalf("SchemaJSONForRuntime: %v", err)
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

func TestSchemaJSONForRuntime_OpenAIRequiresAllCommitMessageProperties(t *testing.T) {
	original := json.RawMessage(`{
		"type":"object",
		"additionalProperties":false,
		"required":["type","subject"],
		"properties":{
			"type":{"type":"string"},
			"scope":{"type":"string","description":"Optional scope"},
			"subject":{"type":"string"},
			"body":{"type":"string","description":"Optional body explaining why and impact"}
		}
	}`)
	prompt := api.Prompt{SchemaJSON: original}
	got, err := SchemaJSONForRuntime(api.OpenAI, api.ModeAgent, prompt)
	if err != nil {
		t.Fatalf("SchemaJSONForRuntime: %v", err)
	}

	originalObject := decodeSchemaObject(t, original)
	if required := schemaRequired(t, originalObject); !reflect.DeepEqual(required, []string{"type", "subject"}) {
		t.Fatalf("original required = %v, want [type subject]", required)
	}
	if originalObject["additionalProperties"] != false {
		t.Fatal("provider transform mutated the original commit-message schema")
	}

	strictObject := decodeSchemaObject(t, got)
	if required := schemaRequired(t, strictObject); !reflect.DeepEqual(required, []string{"body", "scope", "subject", "type"}) {
		t.Fatalf("openai required = %v, want every property in sorted order", required)
	}
	if strictObject["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %v, want false", strictObject["additionalProperties"])
	}
	body := strictObject["properties"].(map[string]any)["body"].(map[string]any)
	if body["type"] != "string" {
		t.Fatalf("optional body type = %v, want string (not nullable)", body["type"])
	}
}

func TestOpenAICompatibleSchema_RecursesWithoutMutatingInput(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"properties":{
			"result":{"type":"object","properties":{"z":{"type":"string"},"a":{"type":"string"}}},
			"rows":{"type":"array","items":{"type":"object","properties":{"value":{"type":"integer"}}}}
		},
		"$defs":{"detail":{"type":"object","properties":{"note":{"type":"string"}}}}
	}`)
	original := append(json.RawMessage(nil), raw...)

	got, err := OpenAICompatibleSchema(raw)
	if err != nil {
		t.Fatalf("OpenAICompatibleSchema: %v", err)
	}
	if string(raw) != string(original) {
		t.Fatal("OpenAICompatibleSchema mutated its input")
	}

	root := decodeSchemaObject(t, got)
	if required := schemaRequired(t, root); !reflect.DeepEqual(required, []string{"result", "rows"}) {
		t.Fatalf("root required = %v", required)
	}
	properties := root["properties"].(map[string]any)
	result := properties["result"].(map[string]any)
	if required := schemaRequired(t, result); !reflect.DeepEqual(required, []string{"a", "z"}) {
		t.Fatalf("nested required = %v", required)
	}
	row := properties["rows"].(map[string]any)["items"].(map[string]any)
	if required := schemaRequired(t, row); !reflect.DeepEqual(required, []string{"value"}) {
		t.Fatalf("array item required = %v", required)
	}
	detail := root["$defs"].(map[string]any)["detail"].(map[string]any)
	if required := schemaRequired(t, detail); !reflect.DeepEqual(required, []string{"note"}) {
		t.Fatalf("$defs required = %v", required)
	}
	for name, object := range map[string]map[string]any{"root": root, "result": result, "row": row, "detail": detail} {
		if object["additionalProperties"] != false {
			t.Errorf("%s additionalProperties = %v, want false", name, object["additionalProperties"])
		}
	}
}

func TestOpenAICompatibleSchema_RemovesAnnotationsFromReferences(t *testing.T) {
	raw := json.RawMessage(`{
		"$schema":"https://json-schema.org/draft/2020-12/schema",
		"$id":"https://example.com/plan-envelope",
		"$ref":"#/$defs/PlanEnvelope",
		"$defs":{
			"PlanEnvelope":{
				"type":"object",
				"properties":{
					"summary":{"type":"string","description":"What the session did"},
					"plan":{"$ref":"#/$defs/PlanResult","description":"The plan this session produced"}
				}
			},
			"PlanResult":{
				"type":"object",
				"properties":{
					"status":{"type":"string"},
					"path":{"type":"string"}
				}
			}
		}
	}`)
	original := append(json.RawMessage(nil), raw...)

	got, err := OpenAICompatibleSchema(raw)
	if err != nil {
		t.Fatalf("OpenAICompatibleSchema: %v", err)
	}
	if string(raw) != string(original) {
		t.Fatal("OpenAICompatibleSchema mutated its input")
	}

	root := decodeSchemaObject(t, got)
	if root["$ref"] != "#/$defs/PlanEnvelope" || root["$schema"] == nil || root["$defs"] == nil {
		t.Fatalf("root reference metadata was not preserved: %#v", root)
	}
	if _, exists := root["$id"]; exists {
		t.Fatalf("root reference retained unsupported $id sibling: %#v", root)
	}
	defs := root["$defs"].(map[string]any)
	planEnvelope := defs["PlanEnvelope"].(map[string]any)
	plan := planEnvelope["properties"].(map[string]any)["plan"].(map[string]any)
	if !reflect.DeepEqual(plan, map[string]any{"$ref": "#/$defs/PlanResult"}) {
		t.Fatalf("plan reference = %#v, want a standalone $ref", plan)
	}
	summary := planEnvelope["properties"].(map[string]any)["summary"].(map[string]any)
	if summary["description"] != "What the session did" {
		t.Fatalf("ordinary property description was removed: %#v", summary)
	}
	for name, object := range map[string]map[string]any{
		"PlanEnvelope": planEnvelope,
		"PlanResult":   defs["PlanResult"].(map[string]any),
	} {
		if object["additionalProperties"] != false {
			t.Errorf("%s additionalProperties = %v, want false", name, object["additionalProperties"])
		}
	}
}

func TestOpenAICompatibleSchema_RejectsSemanticReferenceSiblings(t *testing.T) {
	raw := json.RawMessage(`{
		"type":"object",
		"properties":{"value":{"$ref":"#/$defs/Value","minLength":1}},
		"$defs":{"Value":{"type":"string"}}
	}`)
	_, err := OpenAICompatibleSchema(raw)
	if err == nil || !strings.Contains(err.Error(), `$.properties.value.$ref has unsupported sibling "minLength"`) {
		t.Fatalf("error = %v, want path-aware unsupported sibling error", err)
	}
}

func TestOpenAICompatibleSchema_RejectsOpenEndedObjects(t *testing.T) {
	type labels struct {
		Values map[string]string `json:"values"`
	}
	_, err := SchemaJSONForRuntime(api.OpenAI, api.ModeAPI, api.Prompt{Schema: &labels{}})
	if err == nil || !strings.Contains(err.Error(), "additionalProperties must be false") {
		t.Fatalf("error = %v, want open-ended object rejection", err)
	}
}

// The OpenAI schema subset is a property of a provider×mode cell, not of a
// family: openai's cmux mode hands the schema to the local CLI, which accepts
// the full dialect.
func TestUsesOpenAISchemaSubset(t *testing.T) {
	type runtime struct {
		provider *api.ModelProvider
		mode     api.RuntimeMode
	}
	subset := []runtime{
		{api.OpenAI, api.ModeAPI},
		{api.OpenAI, api.ModeCLI},
		{api.OpenAI, api.ModeAgent},
	}
	full := []runtime{
		{api.Anthropic, api.ModeAPI},
		{api.Google, api.ModeAPI},
		{api.OpenAI, api.ModeCmux},
	}
	for _, r := range subset {
		if !UsesOpenAISchemaSubset(r.provider, r.mode) {
			t.Errorf("UsesOpenAISchemaSubset(%s, %s) = false, want true", r.provider.Name, r.mode)
		}
	}
	for _, r := range full {
		if UsesOpenAISchemaSubset(r.provider, r.mode) {
			t.Errorf("UsesOpenAISchemaSubset(%s, %s) = true, want false", r.provider.Name, r.mode)
		}
	}
}

func TestSchemaJSONForRuntime_NativeTransformNotUsedForGemini(t *testing.T) {
	raw := json.RawMessage(`{"type":"array","maxItems":2}`)
	got, err := SchemaJSONForRuntime(api.Google, api.ModeAPI, api.Prompt{SchemaJSON: raw})
	if err != nil {
		t.Fatalf("SchemaJSONForRuntime: %v", err)
	}
	if string(got) != string(raw) {
		t.Fatalf("gemini schema changed: got %s want %s", got, raw)
	}
}

func decodeSchemaObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	return object
}

func schemaRequired(t *testing.T, object map[string]any) []string {
	t.Helper()
	raw, ok := object["required"].([]any)
	if !ok {
		t.Fatalf("required = %#v, want array", object["required"])
	}
	out := make([]string, len(raw))
	for i, value := range raw {
		var ok bool
		out[i], ok = value.(string)
		if !ok {
			t.Fatalf("required[%d] = %#v, want string", i, value)
		}
	}
	return out
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
