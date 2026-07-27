package api

import (
	"fmt"
	"strings"
)

// TerminalOutcomeKind identifies a native agent terminal result.
type TerminalOutcomeKind string

const (
	TerminalOutcomePlan      TerminalOutcomeKind = "plan"
	TerminalOutcomeQuestions TerminalOutcomeKind = "questions"
)

// TerminalPlan is the plan returned by a native planning tool.
type TerminalPlan struct {
	Content string `json:"content"`
	Path    string `json:"path,omitempty"`
}

// TerminalQuestion is one question returned by a native ask-user tool.
type TerminalQuestion struct {
	Text    string   `json:"text"`
	Context string   `json:"context,omitempty"`
	Options []string `json:"options,omitempty"`
}

// TerminalOutcome carries native plan or question completion independently of
// schema-constrained StructuredData.
type TerminalOutcome struct {
	Kind      TerminalOutcomeKind `json:"kind"`
	Plan      *TerminalPlan       `json:"plan,omitempty"`
	Questions []TerminalQuestion  `json:"questions,omitempty"`
}

// Validate rejects incomplete or mixed terminal payloads.
func (o TerminalOutcome) Validate() error {
	switch o.Kind {
	case TerminalOutcomePlan:
		if o.Plan == nil || strings.TrimSpace(o.Plan.Content) == "" {
			return fmt.Errorf("terminal plan content is required")
		}
		if len(o.Questions) > 0 {
			return fmt.Errorf("plan outcome must not carry questions")
		}
	case TerminalOutcomeQuestions:
		if o.Plan != nil {
			return fmt.Errorf("questions outcome must not carry a plan")
		}
		if len(o.Questions) == 0 {
			return fmt.Errorf("terminal questions are required")
		}
		for i, question := range o.Questions {
			if strings.TrimSpace(question.Text) == "" {
				return fmt.Errorf("terminal question %d text is required", i+1)
			}
		}
	default:
		return fmt.Errorf("unknown terminal outcome kind %q", o.Kind)
	}
	return nil
}
