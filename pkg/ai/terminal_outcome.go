package ai

import (
	"fmt"

	"github.com/flanksource/captain/pkg/api"
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
		questions, err := api.TerminalQuestionsFromInput(event.Input)
		if err != nil {
			return nil, fmt.Errorf("AskUserQuestion: %w", err)
		}
		return &TerminalOutcome{Kind: TerminalOutcomeQuestions, Questions: questions}, nil
	default:
		return nil, nil
	}
}

// PlanTerminalPermission is the shared plan-mode permission policy: in a
// plan-only run the ExitPlanMode call is the turn's terminal signal, not a
// permission to grant — allowing it would leave plan mode and start
// implementation inside the same agent turn. Transports consult this before
// brokering a can_use_tool round-trip; when handled they apply the decision
// themselves (the SDK answers the deny, cmux dismisses its plan surface).
// AskUserQuestion is deliberately not handled: its round-trip is the
// interactive ask flow.
func PlanTerminalPermission(planMode bool, req PermissionRequest) (PermissionDecision, bool) {
	if !planMode || req.Tool != "ExitPlanMode" {
		return PermissionDecision{}, false
	}
	return PermissionDecision{
		Allow:   false,
		Message: "Plan captured for human review. Do not implement it in this session — end the turn and return your final response.",
	}, true
}

func terminalPlanFromInput(input map[string]any) (*TerminalOutcome, error) {
	plan, err := api.TerminalPlanFromInput(input)
	if err != nil {
		return nil, err
	}
	outcome := &TerminalOutcome{Kind: TerminalOutcomePlan, Plan: plan}
	return outcome, outcome.Validate()
}
