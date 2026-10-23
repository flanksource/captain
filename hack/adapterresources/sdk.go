package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/flanksource/captain/pkg/adapters/presentation"
	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/responses"
	"google.golang.org/genai"
)

type sdkSchemas struct {
	prefix      string
	definitions map[string]any
	sources     map[string]sdkSource
}

func enrichAPISchemas() {
	for _, provider := range []struct {
		name, version, docs string
		request             reflect.Type
		extras              map[string]reflect.Type
	}{
		{"anthropic", "github.com/anthropics/anthropic-sdk-go v1.26.0", "https://platform.claude.com/docs/en/api/messages", reflect.TypeFor[anthropic.MessageNewParams](), nil},
		{"openai", "github.com/openai/openai-go v1.12.0", "https://platform.openai.com/docs/api-reference/responses/create", reflect.TypeFor[responses.ResponseNewParams](), nil},
		{"google", "google.golang.org/genai v1.57.0", "https://ai.google.dev/api/generate-content", reflect.TypeFor[genai.GenerateContentConfig](), map[string]reflect.Type{"contents": reflect.TypeFor[[]*genai.Content]()}},
		{"deepseek", "github.com/openai/openai-go v1.12.0", "https://api-docs.deepseek.com/api/create-chat-completion/", reflect.TypeFor[openai.ChatCompletionNewParams](), nil},
	} {
		path := "pkg/adapters/" + provider.name + "/api.schema.json"
		schema := readObject(path)
		builder := &sdkSchemas{prefix: strings.ToUpper(provider.name[:1]) + provider.name[1:] + "API", definitions: map[string]any{}, sources: map[string]sdkSource{}}
		native := builder.structure(provider.request)
		properties := schema["properties"].(map[string]any)
		for key, raw := range native["properties"].(map[string]any) {
			field, exists := properties[key].(map[string]any)
			if !exists {
				field = map[string]any{"readOnly": true, "x-clicky-section": "environment",
					"x-captain-option": map[string]any{"nativeName": key, "support": "unsupported",
						"note": "Declared by " + provider.version + "; Captain does not construct this request option."}}
				properties[key] = field
			}
			shape := raw.(map[string]any)
			if provider.name == "anthropic" && key == "model" {
				delete(field, "enum")
				delete(field, "x-enum-labels")
				delete(field, "x-enum-display")
			}
			for _, keyword := range []string{"type", "items", "properties", "additionalProperties", "oneOf", "anyOf", "$ref", "goJSONSchema"} {
				delete(field, keyword)
			}
			for keyword, value := range shape {
				if keyword != "title" && keyword != "description" {
					field[keyword] = value
				}
			}
		}
		for key, kind := range provider.extras {
			field := properties[key].(map[string]any)
			shape := builder.value(kind)
			for keyword, value := range shape {
				field[keyword] = value
			}
		}
		schema["$defs"] = builder.definitions
		schema["x-captain-inventory"] = map[string]any{"sourceType": provider.request.String(), "version": provider.version}
		presentation.AnnotateSchema(schema, provider.docs)
		markNativeUnions(schema)
		writeObject(path, schema)
		fmt.Printf("%s: %d native request definitions from %s\n", provider.name, len(builder.definitions), provider.version)
	}
}

func (b *sdkSchemas) value(kind reflect.Type) map[string]any {
	for kind.Kind() == reflect.Pointer {
		kind = kind.Elem()
	}
	if kind == reflect.TypeFor[json.RawMessage]() || kind.Kind() == reflect.Interface {
		return map[string]any{"format": "json"}
	}
	if kind == reflect.TypeFor[time.Time]() {
		return map[string]any{"type": "string", "format": "date-time"}
	}
	if strings.Contains(kind.PkgPath(), "/param") && strings.HasPrefix(kind.Name(), "Opt[") {
		field, exists := kind.FieldByName("Value")
		if !exists {
			panic("native optional type has no Value: " + kind.String())
		}
		return b.value(field.Type)
	}
	if strings.HasSuffix(kind.PkgPath(), "/constant") {
		raw, err := json.Marshal(reflect.Zero(kind).Interface())
		must(err)
		var value any
		must(json.Unmarshal(raw, &value))
		return map[string]any{"const": value}
	}
	if kind.Kind() == reflect.Struct && kind.Name() != "" {
		name := b.prefix + kind.Name()
		if _, exists := b.definitions[name]; !exists {
			b.definitions[name] = map[string]any{}
			b.definitions[name] = b.structure(kind)
		}
		return map[string]any{"$ref": "#/$defs/" + name}
	}
	switch kind.Kind() {
	case reflect.String:
		values := b.source(kind.PkgPath()).enums[kind.Name()]
		if kind == reflect.TypeFor[anthropic.Model]() {
			return map[string]any{"type": "string", "examples": values}
		}
		if len(values) > 0 {
			return map[string]any{"type": "string", "enum": values}
		}
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice, reflect.Array:
		if kind.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "contentEncoding": "base64"}
		}
		return map[string]any{"type": "array", "items": b.value(kind.Elem())}
	case reflect.Map:
		if kind.Key().Kind() != reflect.String {
			panic("non-string native map key: " + kind.String())
		}
		return map[string]any{"type": "object", "additionalProperties": b.value(kind.Elem())}
	case reflect.Struct:
		return b.structure(kind)
	default:
		panic("unrepresentable native request type: " + kind.String())
	}
}

func (b *sdkSchemas) structure(kind reflect.Type) map[string]any {
	properties := map[string]any{}
	var required, alternatives []any
	source := b.source(kind.PkgPath())
	for i := 0; i < kind.NumField(); i++ {
		field := kind.Field(i)
		if !field.IsExported() {
			continue
		}
		tag := strings.Split(field.Tag.Get("json"), ",")
		if tag[0] == "-" {
			continue
		}
		if contains(tag, "inline") {
			alternatives = append(alternatives, b.value(field.Type))
			continue
		}
		if tag[0] == "" {
			panic("native field has no JSON name: " + kind.String() + "." + field.Name)
		}
		value := b.value(field.Type)
		if description := source.fields[kind.Name()+"."+field.Name]; description != "" {
			value["description"] = description
		}
		properties[tag[0]] = value
		if contains(tag, "required") {
			required = append(required, tag[0])
		}
	}
	if len(alternatives) > 0 {
		if len(properties) > 0 {
			panic("native union mixes inline variants and properties: " + kind.String())
		}
		return map[string]any{"anyOf": alternatives}
	}
	result := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if len(required) > 0 {
		result["required"] = required
	}
	if description := source.types[kind.Name()]; description != "" {
		result["description"] = description
	}
	return result
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
