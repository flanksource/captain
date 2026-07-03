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
