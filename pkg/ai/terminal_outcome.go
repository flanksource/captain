package ai

import (
	"fmt"
	"strings"
)

// TerminalOutcomeFromEvent normalizes supported native terminal tool events.
func TerminalOutcomeFromEvent(event Event) (*TerminalOutcome, error) {
	if event.Kind != EventToolUse {
		return nil, nil
	}
	switch event.Tool {
	case "ExitPlanMode":
		return terminalPlanFromInput(event.Input)
	case "AskUserQuestion":
		return terminalQuestionsFromInput(event.Input)
	default:
		return nil, nil
	}
}

func terminalPlanFromInput(input map[string]any) (*TerminalOutcome, error) {
	content, err := requiredString(input, "plan")
	if err != nil {
		return nil, fmt.Errorf("ExitPlanMode: %w", err)
	}
	path, err := optionalString(input, "planFilePath")
	if err != nil {
		return nil, fmt.Errorf("ExitPlanMode: %w", err)
	}
	outcome := &TerminalOutcome{
		Kind: TerminalOutcomePlan,
		Plan: &TerminalPlan{Content: content, Path: path},
	}
	return outcome, outcome.Validate()
}

func terminalQuestionsFromInput(input map[string]any) (*TerminalOutcome, error) {
	raw, ok := input["questions"]
	if !ok {
		question, err := terminalQuestionFromMap(input)
		if err != nil {
			return nil, fmt.Errorf("AskUserQuestion: %w", err)
		}
		outcome := &TerminalOutcome{Kind: TerminalOutcomeQuestions, Questions: []TerminalQuestion{question}}
		return outcome, outcome.Validate()
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("AskUserQuestion: questions must be an array, got %T", raw)
	}
	questions := make([]TerminalQuestion, 0, len(items))
	for i, item := range items {
		values, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("AskUserQuestion: question %d must be an object, got %T", i+1, item)
		}
		question, err := terminalQuestionFromMap(values)
		if err != nil {
			return nil, fmt.Errorf("AskUserQuestion: question %d: %w", i+1, err)
		}
		questions = append(questions, question)
	}
	outcome := &TerminalOutcome{Kind: TerminalOutcomeQuestions, Questions: questions}
	return outcome, outcome.Validate()
}

func terminalQuestionFromMap(values map[string]any) (TerminalQuestion, error) {
	text, err := firstRequiredString(values, "question", "prompt", "text")
	if err != nil {
		return TerminalQuestion{}, err
	}
	context, err := firstOptionalString(values, "context", "header")
	if err != nil {
		return TerminalQuestion{}, err
	}
	options, err := terminalQuestionOptions(values["options"])
	if err != nil {
		return TerminalQuestion{}, err
	}
	return TerminalQuestion{Text: text, Context: context, Options: options}, nil
}

func terminalQuestionOptions(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("options must be an array, got %T", raw)
	}
	options := make([]string, 0, len(items))
	for i, item := range items {
		var option string
		var err error
		switch value := item.(type) {
		case string:
			option = strings.TrimSpace(value)
		case map[string]any:
			option, err = firstRequiredString(value, "label", "value")
		default:
			err = fmt.Errorf("must be a string or object, got %T", item)
		}
		if err != nil || option == "" {
			if err == nil {
				err = fmt.Errorf("must not be empty")
			}
			return nil, fmt.Errorf("option %d %w", i+1, err)
		}
		options = append(options, option)
	}
	return options, nil
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

func firstRequiredString(values map[string]any, keys ...string) (string, error) {
	text, err := firstOptionalString(values, keys...)
	if err != nil {
		return "", err
	}
	if text == "" {
		return "", fmt.Errorf("one of %s is required", strings.Join(keys, ", "))
	}
	return text, nil
}

func firstOptionalString(values map[string]any, keys ...string) (string, error) {
	for _, key := range keys {
		value, ok := values[key]
		if !ok || value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return "", fmt.Errorf("%s must be a string, got %T", key, value)
		}
		if strings.TrimSpace(text) != "" {
			return text, nil
		}
	}
	return "", nil
}
