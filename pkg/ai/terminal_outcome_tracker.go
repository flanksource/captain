package ai

import "slices"

// TerminalOutcomeTracker retains native questions only until their matching
// result arrives. Plan capture remains terminal even when ExitPlanMode is denied.
type TerminalOutcomeTracker struct {
	plan      *TerminalOutcome
	questions []pendingQuestion
}

type pendingQuestion struct {
	id      string
	outcome *TerminalOutcome
}

func (t *TerminalOutcomeTracker) Observe(event Event) error {
	if event.ParentToolCallID != "" {
		return nil
	}
	if event.ToolCallID != "" && (event.Kind == EventToolResult || event.Kind == EventToolUse) {
		t.questions = slices.DeleteFunc(t.questions, func(q pendingQuestion) bool { return q.id == event.ToolCallID })
	}
	outcome, err := TerminalOutcomeFromEvent(event)
	if err != nil {
		return err
	}
	if outcome != nil {
		if outcome.Kind == TerminalOutcomeQuestions {
			t.questions = append(t.questions, pendingQuestion{id: event.ToolCallID, outcome: outcome})
		} else {
			t.plan = outcome
		}
	}
	return nil
}

func (t *TerminalOutcomeTracker) Outcome() *TerminalOutcome {
	if len(t.questions) == 0 {
		return t.plan
	}
	outcome := &TerminalOutcome{Kind: TerminalOutcomeQuestions}
	for _, pending := range t.questions {
		outcome.Questions = append(outcome.Questions, pending.outcome.Questions...)
	}
	return outcome
}
