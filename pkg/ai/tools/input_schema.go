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

// inlineSchemaRefs copies node with every `#/$defs/<name>` $ref replaced by its
// definition. Keywords beside a $ref (a description) override the definition's.
// A recursive definition cannot be inlined and fails.
func inlineSchemaRefs(node any, defs map[string]any, visiting []string) (any, error) {
	switch value := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		if ref, ok := value["$ref"].(string); ok {
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
		for key, child := range value {
			if key == "$ref" || key == "$defs" {
				continue
			}
			copied, err := inlineSchemaRefs(child, defs, visiting)
			if err != nil {
				return nil, err
			}
			out[key] = copied
		}
		return out, nil
	case []any:
		out := make([]any, len(value))
		for i, child := range value {
			copied, err := inlineSchemaRefs(child, defs, visiting)
			if err != nil {
				return nil, err
			}
			out[i] = copied
		}
		return out, nil
	default:
		return node, nil
	}
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
