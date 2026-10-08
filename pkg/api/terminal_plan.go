package api

import (
	"fmt"
	"strings"
)

// TerminalPlanFromInput reads ExitPlanMode's input: the plan text, and the plan
// file it was written to when the agent names one.
func TerminalPlanFromInput(input map[string]any) (*TerminalPlan, error) {
	content, err := requiredString(input, "plan")
	if err != nil {
		return nil, fmt.Errorf("ExitPlanMode: %w", err)
	}
	path, err := optionalString(input, "planFilePath")
	if err != nil {
		return nil, fmt.Errorf("ExitPlanMode: %w", err)
	}
	return &TerminalPlan{Content: content, Path: path}, nil
}

func requiredString(values map[string]any, key string) (string, error) {
	value, ok := values[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", key, value)
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return text, nil
}

func optionalString(values map[string]any, key string) (string, error) {
	value, ok := values[key]
	if !ok || value == nil {
		return "", nil
	}
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string, got %T", key, value)
	}
	return text, nil
}
