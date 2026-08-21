package api

import (
	"encoding/json"
	"testing"
)

// schemaDefs reflects the spec and returns its $defs, which is where every named
// type lands.
func schemaDefs(t *testing.T) map[string]any {
	t.Helper()
	data, err := SchemaJSON(&Spec{})
	if err != nil {
		t.Fatalf("SchemaJSON: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no $defs:\n%s", data)
	}
	return defs
}

func def(t *testing.T, defs map[string]any, name string) map[string]any {
	t.Helper()
	d, ok := defs[name].(map[string]any)
	if !ok {
		t.Fatalf("schema has no definition for %s", name)
	}
	return d
}

func enumOf(t *testing.T, schema map[string]any) []string {
	t.Helper()
	raw, ok := schema["enum"].([]any)
	if !ok {
		t.Fatalf("schema has no enum: %v", schema)
	}
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i], _ = v.(string)
	}
	return out
}

// TestPermissionEnumsAreDescribed pins that every permission enum ships its
// values in the schema. Each of these is a plain string type, so reflection alone
// emits `{"type":"string"}` — a schema that validates "banana" and hands a client
// nothing to render, which is why the editor grew its own hardcoded copies.
// A named type reached only through additionalProperties is inlined rather than
// given a $defs entry, so each case says where its enum actually lands.
func TestPermissionEnumsAreDescribed(t *testing.T) {
	defs := schemaDefs(t)
	nested := func(outer string, path ...string) func(*testing.T) map[string]any {
		return func(t *testing.T) map[string]any {
			t.Helper()
			schema := def(t, defs, outer)
			for _, key := range path {
				next, ok := schema[key].(map[string]any)
				if !ok {
					t.Fatalf("%s has no %q: %v", outer, key, schema)
				}
				schema = next
			}
			return schema
		}
	}
	cases := []struct {
		name string
		at   func(*testing.T) map[string]any
		want []string
	}{
		{"PermissionMode", func(t *testing.T) map[string]any { return def(t, defs, "PermissionMode") }, stringsOf(AllPermissionModes())},
		{"Preset", func(t *testing.T) map[string]any { return def(t, defs, "Preset") }, stringsOf([]Preset{PresetEdit, PresetBare})},
		{"ToolPolicy", nested("Tools", "additionalProperties"), stringsOf(AllToolPolicies())},
		{"ResourceMode", nested("MCP", "properties", "modes", "additionalProperties"), stringsOf(AllResourceModes())},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := enumOf(t, tc.at(t))
			if len(got) != len(tc.want) {
				t.Fatalf("%s enum = %v, want %v", tc.name, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("%s enum = %v, want %v", tc.name, got, tc.want)
				}
			}
		})
	}
}

// TestToolsAndMCPAreNotEmptySchemas is the one that matters most. Tools and MCP
// carry every field as json:"-" behind hand-written marshalers, so reflection
// reported `{}` for both: the two fields that decide what an agent may do
// validated anything at all and told a client nothing.
func TestToolsAndMCPAreNotEmptySchemas(t *testing.T) {
	defs := schemaDefs(t)

	tools := def(t, defs, "Tools")
	if tools["type"] != "object" {
		t.Fatalf("Tools should be an object map, got %v", tools)
	}
	policy, ok := tools["additionalProperties"].(map[string]any)
	if !ok {
		t.Fatalf("Tools should constrain its values to a policy, got %v", tools)
	}
	if got := enumOf(t, policy); len(got) != len(AllToolPolicies()) {
		t.Fatalf("Tools values should enumerate every policy, got %v", got)
	}

	mcp := def(t, defs, "MCP")
	properties, ok := mcp["properties"].(map[string]any)
	if !ok {
		t.Fatalf("MCP should declare its properties, got %v", mcp)
	}
	for _, field := range []string{"disabled", "servers", "modes"} {
		if _, ok := properties[field]; !ok {
			t.Fatalf("MCP schema is missing %q: %v", field, properties)
		}
	}
}

// TestResourcePoliciesAcceptsBothWireForms pins the array shorthand. It has
// always decoded, but reflection saw only the map half, so a document using the
// legacy list form failed validation against captain's own schema.
func TestResourcePoliciesAcceptsBothWireForms(t *testing.T) {
	schema := def(t, schemaDefs(t), "ResourcePolicies")
	oneOf, ok := schema["oneOf"].([]any)
	if !ok || len(oneOf) != 2 {
		t.Fatalf("ResourcePolicies should declare both wire forms, got %v", schema)
	}
	kinds := make(map[string]bool, 2)
	for _, branch := range oneOf {
		if m, ok := branch.(map[string]any); ok {
			kinds[m["type"].(string)] = true
		}
	}
	if !kinds["object"] || !kinds["array"] {
		t.Fatalf("ResourcePolicies should accept an object or an array, got %v", oneOf)
	}
}

func stringsOf[T ~string](values []T) []string {
	out := make([]string, len(values))
	for i, v := range values {
		out[i] = string(v)
	}
	return out
}
