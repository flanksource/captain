package tools

import (
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

// inputSchema returns the schema a provider sends for a caller tool: a copy of
// the caller's schema as a self-contained root object. Anthropic input_schema,
// OpenAI function parameters and MCP inputSchema all require a root object, and
// genkit's Anthropic plugin keeps only a schema's type, properties and required
// keys — a reflected `{"$ref": "#/$defs/X", "$defs": …}` reached the API with
// no input_schema at all. So $refs are inlined rather than $defs carried along.
func inputSchema(name string, schema map[string]any) (map[string]any, error) {
	if schema == nil {
		return api.ObjectSchema(nil), nil
	}
	defs, _ := schema["$defs"].(map[string]any)
	resolved, err := inlineSchemaRefs(schema, defs, nil)
	if err != nil {
		return nil, fmt.Errorf("caller tool %q input schema: %w", name, err)
	}
	root := resolved.(map[string]any)
	delete(root, "$schema")
	delete(root, "$id")
	root = api.ObjectSchema(root)
	if root["type"] != "object" {
		return nil, fmt.Errorf("caller tool %q input schema must be a JSON object schema, got type %q", name, fmt.Sprint(root["type"]))
	}
	return root, nil
}

// Keywords whose value is a schema, a list of schemas, or a map of name → schema.
// Every other keyword (const, default, enum, examples, …) holds literal instance
// data, and a properties key is an instance property name, so neither is ever
// read as a $ref.
var (
	subschemaKeywords = []string{"items", "additionalItems", "additionalProperties", "contains",
		"propertyNames", "not", "if", "then", "else", "unevaluatedItems", "unevaluatedProperties"}
	subschemaListKeywords = []string{"allOf", "anyOf", "oneOf", "prefixItems", "items"}
	subschemaMapKeywords  = []string{"properties", "patternProperties", "dependentSchemas"}
)

// inlineSchemaRefs copies the schema node with every `#/$defs/<name>` $ref
// replaced by its definition. Keywords beside a $ref (a description) override
// the definition's. A recursive definition cannot be inlined and fails.
func inlineSchemaRefs(node any, defs map[string]any, visiting []string) (any, error) {
	schema, ok := node.(map[string]any)
	if !ok {
		return node, nil // a boolean schema
	}
	out := make(map[string]any, len(schema))
	if ref, ok := schema["$ref"].(string); ok {
		def, err := schemaDefinition(ref, defs, visiting)
		if err != nil {
			return nil, err
		}
		resolved, err := inlineSchemaRefs(def, defs, append(slices.Clip(visiting), ref))
		if err != nil {
			return nil, err
		}
		out = resolved.(map[string]any)
	}
	for key, child := range schema {
		if key == "$ref" || key == "$defs" {
			continue
		}
		copied, err := inlineSchemaKeyword(key, child, defs, visiting)
		if err != nil {
			return nil, err
		}
		out[key] = copied
	}
	return out, nil
}

func inlineSchemaKeyword(key string, value any, defs map[string]any, visiting []string) (any, error) {
	if list, ok := value.([]any); ok && slices.Contains(subschemaListKeywords, key) {
		out := make([]any, len(list))
		for i, child := range list {
			copied, err := inlineSchemaRefs(child, defs, visiting)
			if err != nil {
				return nil, err
			}
			out[i] = copied
		}
		return out, nil
	}
	if named, ok := value.(map[string]any); ok && slices.Contains(subschemaMapKeywords, key) {
		out := make(map[string]any, len(named))
		for name, child := range named {
			copied, err := inlineSchemaRefs(child, defs, visiting)
			if err != nil {
				return nil, err
			}
			out[name] = copied
		}
		return out, nil
	}
	if slices.Contains(subschemaKeywords, key) {
		return inlineSchemaRefs(value, defs, visiting)
	}
	return value, nil
}

func schemaDefinition(ref string, defs map[string]any, visiting []string) (map[string]any, error) {
	name, ok := strings.CutPrefix(ref, "#/$defs/")
	if !ok {
		return nil, fmt.Errorf("unsupported $ref %q: only local #/$defs references can be inlined", ref)
	}
	if slices.Contains(visiting, ref) {
		return nil, fmt.Errorf("recursive $ref %q cannot be inlined", ref)
	}
	def, ok := defs[name].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("$ref %q has no object definition in $defs", ref)
	}
	return def, nil
}
