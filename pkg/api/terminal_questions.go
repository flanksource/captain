package api

import (
	"fmt"
	"strings"
)

// TerminalQuestionsFromInput normalizes the questions an agent asked into
// TerminalQuestions. It accepts AskUserQuestion's input — a questions array or a
// single question at the top level — and a result envelope's questions, which
// share the shape. Input that cannot be asked fails rather than yielding nothing.
func TerminalQuestionsFromInput(input map[string]any) ([]TerminalQuestion, error) {
	raw, ok := input["questions"]
	if !ok {
		question, err := terminalQuestionFromMap(input)
		if err != nil {
			return nil, err
		}
		return validatedQuestions([]TerminalQuestion{question})
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("questions must be an array, got %T", raw)
	}
	questions := make([]TerminalQuestion, 0, len(items))
	for i, item := range items {
		values, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("question %d must be an object, got %T", i+1, item)
		}
		question, err := terminalQuestionFromMap(values)
		if err != nil {
			return nil, fmt.Errorf("question %d: %w", i+1, err)
		}
		questions = append(questions, question)
	}
	return validatedQuestions(questions)
}

func validatedQuestions(questions []TerminalQuestion) ([]TerminalQuestion, error) {
	if err := (TerminalOutcome{Kind: TerminalOutcomeQuestions, Questions: questions}).Validate(); err != nil {
		return nil, err
	}
	return questions, nil
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
	id, err := firstOptionalString(values, "id")
	if err != nil {
		return TerminalQuestion{}, err
	}
	question := TerminalQuestion{ID: id, Text: text, Context: context}
	if question.MultiSelect, err = firstOptionalBool(values, "multiSelect", "multi_select"); err != nil {
		return TerminalQuestion{}, err
	}
	if err := addTerminalOptions(&question, values["options"]); err != nil {
		return TerminalQuestion{}, err
	}
	return question, nil
}

// addTerminalOptions puts each option's label on the question, and the
// description of every option the agent described.
func addTerminalOptions(question *TerminalQuestion, raw any) error {
	if raw == nil {
		return nil
	}
	items, ok := raw.([]any)
	if !ok {
		return fmt.Errorf("options must be an array, got %T", raw)
	}
	for i, item := range items {
		label, description, err := terminalOption(item)
		if err != nil {
			return fmt.Errorf("option %d %w", i+1, err)
		}
		question.Options = append(question.Options, label)
		if description == "" {
			continue
		}
		if question.OptionDescriptions == nil {
			question.OptionDescriptions = map[string]string{}
		}
		question.OptionDescriptions[label] = description
	}
	return nil
}

func terminalOption(item any) (label, description string, err error) {
	switch value := item.(type) {
	case string:
		if label = strings.TrimSpace(value); label == "" {
			return "", "", fmt.Errorf("must not be empty")
		}
		return label, "", nil
	case map[string]any:
		if label, err = firstRequiredString(value, "label", "value"); err != nil {
			return "", "", err
		}
		description, err = firstOptionalString(value, "description")
		return label, description, err
	default:
		return "", "", fmt.Errorf("must be a string or object, got %T", item)
	}
}

func firstOptionalBool(values map[string]any, keys ...string) (bool, error) {
	for _, key := range keys {
		value, ok := values[key]
		if !ok || value == nil {
			continue
		}
		flag, ok := value.(bool)
		if !ok {
			return false, fmt.Errorf("%s must be a boolean, got %T", key, value)
		}
		return flag, nil
	}
	return false, nil
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
		if trimmed := strings.TrimSpace(text); trimmed != "" {
			return trimmed, nil
		}
	}
	return "", nil
}
