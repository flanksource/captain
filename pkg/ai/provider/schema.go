package provider

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

type JSONSchema struct {
	Type                 string                `json:"type,omitempty"`
	Properties           map[string]JSONSchema `json:"properties,omitempty"`
	Required             []string              `json:"required,omitempty"`
	Items                *JSONSchema           `json:"items,omitempty"`
	Description          string                `json:"description,omitempty"`
	Enum                 []any                 `json:"enum,omitempty"`
	AdditionalProperties bool                  `json:"additionalProperties,omitempty"`
}

func GenerateJSONSchema(v any) (*JSONSchema, error) {
	t := reflect.TypeOf(v)
	if t == nil {
		return nil, fmt.Errorf("cannot generate schema from nil")
	}
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("schema generation requires a struct type, got %s", t.Kind())
	}
	return buildSchema(t), nil
}

func buildSchema(t reflect.Type) *JSONSchema {
	for t.Kind() == reflect.Ptr {
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

func SchemaToJSON(schema *JSONSchema) (string, error) {
	data, err := json.Marshal(schema)
	if err != nil {
		return "", fmt.Errorf("failed to marshal schema: %w", err)
	}
	return string(data), nil
}
