package api

import (
	"fmt"
	"strconv"
	"strings"
)

// TerminalAnswer is one question's resolved choices, in the order the agent
// asked. Providers shape these into whatever their host expects: Codex keys its
// payload by question id, Claude by question text.
type TerminalAnswer struct {
	Question TerminalQuestion
	// Index is 1-based — the position the agent asked the question in.
	Index   int
	Choices []string
}

// AnswersForQuestions resolves a submitted answers map against the questions the
// agent asked. A key matches a question by id, by 1-based index, or by exact
// text, because those are the three identities hosts key answers on and a
// dashboard only knows the ones the agent actually supplied.
//
// Every question must be answered exactly once and no key may go unmatched: a
// host that cannot place a key rejects the whole payload, so accepting it here
// would only lose the answers further downstream.
func AnswersForQuestions(questions []TerminalQuestion, submitted any) ([]TerminalAnswer, error) {
	if len(questions) == 0 {
		return nil, fmt.Errorf("no questions to answer")
	}
	values, ok := submitted.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("answers must be an object keyed by question id, index or text, got %T", submitted)
	}
	answers := make([]TerminalAnswer, 0, len(questions))
	used := make(map[string]struct{}, len(values))
	for i, question := range questions {
		key, value, err := answerFor(values, used, question, i+1)
		if err != nil {
			return nil, err
		}
		choices, err := answerChoices(question, value)
		if err != nil {
			return nil, err
		}
		used[key] = struct{}{}
		answers = append(answers, TerminalAnswer{Question: question, Index: i + 1, Choices: choices})
	}
	for key := range values {
		if _, matched := used[key]; !matched {
			return nil, fmt.Errorf("answer %q does not name any question", key)
		}
	}
	return answers, nil
}

// answerFor finds the one key naming this question. Identities are tried
// strongest first so a question whose id happens to look like another's index
// still claims its own answer.
func answerFor(values map[string]any, used map[string]struct{}, question TerminalQuestion, index int) (string, any, error) {
	for _, key := range []string{question.ID, strconv.Itoa(index), question.Text} {
		if key == "" {
			continue
		}
		if _, taken := used[key]; taken {
			continue
		}
		if value, exists := values[key]; exists {
			return key, value, nil
		}
	}
	return "", nil, fmt.Errorf("question %d %q has no answer", index, question.Text)
}

func answerChoices(question TerminalQuestion, value any) ([]string, error) {
	switch answer := value.(type) {
	case string:
		return trimmedChoices(question, []string{answer})
	case []string:
		if !question.MultiSelect {
			return nil, fmt.Errorf("question %q takes one answer, not a list", question.Text)
		}
		return trimmedChoices(question, answer)
	case []any:
		if !question.MultiSelect {
			return nil, fmt.Errorf("question %q takes one answer, not a list", question.Text)
		}
		choices := make([]string, 0, len(answer))
		for _, item := range answer {
			text, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("question %q has a non-text answer %T", question.Text, item)
			}
			choices = append(choices, text)
		}
		return trimmedChoices(question, choices)
	default:
		return nil, fmt.Errorf("question %q has an unsupported answer type %T", question.Text, value)
	}
}

func trimmedChoices(question TerminalQuestion, choices []string) ([]string, error) {
	if len(choices) == 0 {
		return nil, fmt.Errorf("question %q has no answer", question.Text)
	}
	trimmed := make([]string, 0, len(choices))
	for _, choice := range choices {
		text := strings.TrimSpace(choice)
		if text == "" {
			return nil, fmt.Errorf("question %q has a blank answer", question.Text)
		}
		trimmed = append(trimmed, text)
	}
	return trimmed, nil
}
