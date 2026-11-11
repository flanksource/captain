package cli

import (
	"encoding/json"
	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"maps"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
)

func PromptFormSchema(detail PromptDetail) (map[string]any, error) {
	raw, err := promptInputSchema(detail)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		names, err := promptlib.Load(detail.Content).InputVariables()
		if err != nil {
			return nil, err
		}
		properties := map[string]any{}
		for _, name := range names {
			properties[name] = map[string]any{"type": "string"}
		}
		return map[string]any{"type": "object", "properties": properties, "additionalProperties": false}, nil
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, err
	}
	return schema, nil
}

func validatePromptVariables(detail PromptDetail, supplied map[string]any) (map[string]any, error) {
	schema, err := PromptFormSchema(detail)
	if err != nil {
		return nil, err
	}
	values := maps.Clone(detail.InputDefault)
	if values == nil {
		values = map[string]any{}
	}
	for key, value := range supplied {
		values[key] = value
	}
	properties, _ := schema["properties"].(map[string]any)
	for key, property := range properties {
		if _, exists := values[key]; exists {
			continue
		}
		if object, ok := property.(map[string]any); ok {
			if value, exists := object["default"]; exists {
				values[key] = value
			}
		}
	}
	compiler := validator.NewCompiler()
	if err := compiler.AddResource("urn:captain:prompt-input", schema); err != nil {
		return nil, err
	}
	compiled, err := compiler.Compile("urn:captain:prompt-input")
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(values)
	if err != nil {
		return nil, err
	}
	var normalized any
	if err := json.Unmarshal(data, &normalized); err != nil {
		return nil, err
	}
	if err := compiled.Validate(normalized); err != nil {
		return nil, err
	}
	return values, nil
}
