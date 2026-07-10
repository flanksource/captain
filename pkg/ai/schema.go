package ai

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

// JSONSchema is captain's minimal JSON Schema rendering of a Go struct: enough of
// the vocabulary (types, properties, required, items, enums) for the structured
// output providers to describe the shape a model must return.
type JSONSchema struct {
	Type                 string                `json:"type,omitempty"`
	Properties           map[string]JSONSchema `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	Items                *JSONSchema           `json:"items,omitempty"`
	Description          string                `json:"description,omitempty"`
	Enum                 []any                 `json:"enum,omitempty"`
	AdditionalProperties bool                  `json:"additionalProperties,omitempty"`
}

// GenerateJSONSchema derives a JSONSchema from a Go struct value (or pointer to
// one). It is the single source of truth for the schema captain sends to
// structured-output backends and prints in the logging middleware.
func GenerateJSONSchema(v any) (*JSONSchema, error) {
	t := reflect.TypeOf(v)
	if t == nil {
		return nil, fmt.Errorf("cannot generate schema from nil")
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("schema generation requires a struct type, got %s", t.Kind())
	}
	return buildSchema(t), nil
}

func buildSchema(t reflect.Type) *JSONSchema {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	schema := &JSONSchema{}

	switch t.Kind() {
	case reflect.Struct:
		schema.Type = "object"
		schema.Properties = make(map[string]JSONSchema)
		var required []string

		for i := range t.NumField() {
			field := t.Field(i)
			if !field.IsExported() {
				continue
			}

			jsonTag := field.Tag.Get("json")
			if jsonTag == "-" {
				continue
			}

			fieldName := field.Name
			isRequired := true

			if jsonTag != "" {
				parts := strings.Split(jsonTag, ",")
				if parts[0] != "" {
					fieldName = parts[0]
				}
				for _, part := range parts[1:] {
					if part == "omitempty" {
						isRequired = false
					}
				}
			}

			fieldSchema := buildSchema(field.Type)
			if desc := field.Tag.Get("description"); desc != "" {
				fieldSchema.Description = desc
			}
			schema.Properties[fieldName] = *fieldSchema
			if isRequired {
				required = append(required, fieldName)
			}
		}
		if len(required) > 0 {
			schema.Required = required
		}

	case reflect.Slice, reflect.Array:
		schema.Type = "array"
		schema.Items = buildSchema(t.Elem())

	case reflect.Map:
		schema.Type = "object"
		schema.AdditionalProperties = true

	case reflect.String:
		schema.Type = "string"

	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		schema.Type = "integer"

	case reflect.Float32, reflect.Float64:
		schema.Type = "number"

	case reflect.Bool:
		schema.Type = "boolean"

	case reflect.Interface:
		return &JSONSchema{}

	default:
		schema.Type = "string"
	}

	return schema
}

// SchemaToJSON marshals a JSONSchema to its JSON text.
func SchemaToJSON(schema *JSONSchema) (string, error) {
	data, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("failed to marshal schema: %w", err)
	}
	return string(data), nil
}

// SchemaJSONFor resolves the JSON schema a provider should send the model for a
// prompt: Prompt.SchemaJSON verbatim when set (preserving the full JSON Schema
// vocabulary), otherwise the reflected Prompt.Schema, otherwise nil for a
// text-mode request. It is the single entry point every provider uses so the two
// schema mechanisms behave identically across backends.
func SchemaJSONFor(p api.Prompt) (json.RawMessage, error) {
	if len(p.SchemaJSON) > 0 {
		return p.SchemaJSON, nil
	}
	if p.Schema == nil {
		return nil, nil
	}
	schema, err := GenerateJSONSchema(p.Schema)
	if err != nil {
		return nil, err
	}
	return json.Marshal(schema)
}

// SchemaJSONForBackend resolves the schema a provider should send to its
// backend. Anthropic native structured-output backends receive a transformed
// subset of the caller's original JSON Schema; every other backend receives the
// original schema from SchemaJSONFor.
func SchemaJSONForBackend(backend api.Backend, p api.Prompt) (json.RawMessage, error) {
	schema, err := SchemaJSONFor(p)
	if err != nil || len(schema) == 0 || !UsesAnthropicSchemaSubset(backend) {
		return schema, err
	}
	return AnthropicCompatibleSchema(schema)
}

// UsesAnthropicSchemaSubset reports whether a backend sends JSON Schema to
// Claude's native structured-output machinery and therefore needs Captain to
// apply Anthropic's supported-subset transformation before dispatch.
func UsesAnthropicSchemaSubset(backend api.Backend) bool {
	switch backend {
	case api.BackendAnthropic, api.BackendClaudeAgent, api.BackendClaudeCLI:
		return true
	default:
		return false
	}
}

// AnthropicCompatibleSchema returns a copy of schema with constraints that
// Claude structured outputs do not accept removed from the provider-facing
// payload. The original schema must still be used for local validation.
func AnthropicCompatibleSchema(schema json.RawMessage) (json.RawMessage, error) {
	var v any
	if err := json.Unmarshal(schema, &v); err != nil {
		return nil, fmt.Errorf("anthropic schema transform: invalid JSON schema: %w", err)
	}
	sanitizeAnthropicSchema(v)
	out, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("anthropic schema transform: marshal schema: %w", err)
	}
	return out, nil
}

var anthropicUnsupportedConstraints = map[string]string{
	"minimum":               "minimum",
	"maximum":               "maximum",
	"exclusiveMinimum":      "exclusiveMinimum",
	"exclusiveMaximum":      "exclusiveMaximum",
	"multipleOf":            "multipleOf",
	"minLength":             "minLength",
	"maxLength":             "maxLength",
	"pattern":               "pattern",
	"minItems":              "minItems",
	"maxItems":              "maxItems",
	"uniqueItems":           "uniqueItems",
	"contains":              "contains",
	"minContains":           "minContains",
	"maxContains":           "maxContains",
	"minProperties":         "minProperties",
	"maxProperties":         "maxProperties",
	"dependentRequired":     "dependentRequired",
	"dependentSchemas":      "dependentSchemas",
	"propertyNames":         "propertyNames",
	"unevaluatedItems":      "unevaluatedItems",
	"unevaluatedProperties": "unevaluatedProperties",
}

var anthropicSupportedFormats = map[string]bool{
	"date":      true,
	"date-time": true,
	"email":     true,
	"hostname":  true,
	"ipv4":      true,
	"ipv6":      true,
	"uri":       true,
	"uuid":      true,
}

func sanitizeAnthropicSchema(v any) {
	switch node := v.(type) {
	case map[string]any:
		var hints []string
		for key, label := range anthropicUnsupportedConstraints {
			if value, ok := node[key]; ok {
				hints = append(hints, label+"="+jsonValue(value))
				delete(node, key)
			}
		}
		if value, ok := node["format"].(string); ok && !anthropicSupportedFormats[value] {
			hints = append(hints, "format="+value)
			delete(node, "format")
		}

		for _, value := range node {
			sanitizeAnthropicSchema(value)
		}
		if isObjectSchema(node) {
			if _, ok := node["additionalProperties"]; !ok {
				node["additionalProperties"] = false
			}
		}
		if len(hints) > 0 {
			node["description"] = appendConstraintDescription(node["description"], hints)
		}
	case []any:
		for _, value := range node {
			sanitizeAnthropicSchema(value)
		}
	}
}

func isObjectSchema(node map[string]any) bool {
	if t, ok := node["type"].(string); ok && t == "object" {
		return true
	}
	if ts, ok := node["type"].([]any); ok {
		for _, t := range ts {
			if s, ok := t.(string); ok && s == "object" {
				return true
			}
		}
	}
	_, hasProperties := node["properties"]
	return hasProperties
}

func appendConstraintDescription(existing any, hints []string) string {
	prefix := strings.TrimSpace(fmt.Sprint(existing))
	if prefix == "" || prefix == "<nil>" {
		return "Constraints preserved for local validation: " + strings.Join(hints, ", ") + "."
	}
	return strings.TrimRight(prefix, ".") + ". Constraints preserved for local validation: " + strings.Join(hints, ", ") + "."
}

func jsonValue(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// BindStructuredOutput unmarshals a provider's validated structured-output JSON
// into the caller's Go target. Missing or malformed output fails loudly with
// ErrSchemaValidation rather than leaving the target zero-valued.
func BindStructuredOutput(target any, raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("%w: no structured output returned", ErrSchemaValidation)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%w: %v", ErrSchemaValidation, err)
	}
	return nil
}
