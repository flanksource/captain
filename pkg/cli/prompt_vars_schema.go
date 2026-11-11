package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/flanksource/clicky"
)

// promptVarsSchema is the --vars flag schema of `prompt run`: the chosen
// prompt's input schema, so `-i` asks for its typed variables once the prompt
// is picked. A prompt without inputs takes no variables.
var promptVarsSchema = clicky.FlagSchema{Scope: []string{"id"}, Load: loadPromptVarsSchema}

func loadPromptVarsSchema(ctx context.Context, values map[string]string) (json.RawMessage, error) {
	detail, err := getPrompt(ctx, values["id"])
	if err != nil {
		return nil, err
	}
	return promptInputSchema(detail)
}

// promptInputSchema is the prompt's input schema with its input defaults as
// the properties' defaults; nil when the prompt declares no inputs.
func promptInputSchema(detail PromptDetail) (json.RawMessage, error) {
	if len(detail.InputSchema) == 0 {
		return nil, nil
	}
	schema := maps.Clone(detail.InputSchema)
	if len(detail.InputDefault) > 0 {
		properties, _ := schema["properties"].(map[string]any)
		withDefaults := make(map[string]any, len(properties))
		for name, prop := range properties {
			object, ok := prop.(map[string]any)
			def, hasDefault := detail.InputDefault[name]
			if ok && hasDefault {
				if _, declared := object["default"]; !declared {
					object = maps.Clone(object)
					object["default"] = def
				}
			}
			withDefaults[name] = object
			if !ok {
				withDefaults[name] = prop
			}
		}
		schema["properties"] = withDefaults
	}
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("prompt %s: input schema: %w", detail.ID, err)
	}
	return raw, nil
}
