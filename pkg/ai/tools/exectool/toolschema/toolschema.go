// Package toolschema converts an inline tool's declared input and output
// (Picoschema or JSON Schema) into the schemas a run uses. It is the
// declaration half of exectool, kept free of the agent runtime so prompt
// validation can share it without an import cycle.
package toolschema

import (
	"encoding/json"
	"fmt"

	dp "github.com/google/dotprompt/go/dotprompt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// Input converts a Picoschema or JSON Schema declaration into the object
// schema a tool takes; no declaration takes an empty object.
func Input(declared any) (map[string]any, error) {
	if declared == nil {
		return api.ObjectSchema(nil), nil
	}
	raw, err := convert("input", declared)
	if err != nil {
		return nil, err
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return nil, fmt.Errorf("input schema: %w", err)
	}
	return schema, nil
}

// Output converts the declared output schema; nil means the tool's stdout is
// returned as text.
func Output(declared any) (json.RawMessage, error) {
	if declared == nil {
		return nil, nil
	}
	raw, err := convert("output", declared)
	if err != nil {
		return nil, err
	}
	// An empty document validates against any schema that compiles.
	if _, err := ai.ValidateStructuredJSON(raw, "{}"); err != nil {
		return nil, fmt.Errorf("output schema: %w", err)
	}
	return raw, nil
}

func convert(section string, declared any) (json.RawMessage, error) {
	schema, err := dp.Picoschema(declared, &dp.PicoschemaOptions{})
	if err != nil {
		return nil, fmt.Errorf("%s schema: %w", section, err)
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("%s schema: %w", section, err)
	}
	return raw, nil
}
