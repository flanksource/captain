package cli

import (
	"encoding/json"
	"fmt"
)

func structuredOutputMap(value any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	if output, ok := value.(map[string]any); ok {
		return output, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode structured output: %w", err)
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		return nil, fmt.Errorf("decode structured output object: %w", err)
	}
	return output, nil
}

func structuredOutputText(text string, output map[string]any) (string, error) {
	if text != "" || output == nil {
		return text, nil
	}
	raw, err := json.Marshal(output)
	if err != nil {
		return "", fmt.Errorf("encode structured output text: %w", err)
	}
	return string(raw), nil
}
