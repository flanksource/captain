package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTerminalOutcomeValidate(t *testing.T) {
	tests := []struct {
		name    string
		outcome TerminalOutcome
		wantErr string
	}{
		{
			name: "plan",
			outcome: TerminalOutcome{
				Kind: TerminalOutcomePlan,
				Plan: &TerminalPlan{Content: "1. Inspect\n2. Change"},
			},
		},
		{
			name: "questions",
			outcome: TerminalOutcome{
				Kind:      TerminalOutcomeQuestions,
				Questions: []TerminalQuestion{{Text: "Which database?", Options: []string{"PostgreSQL", "SQLite"}}},
			},
		},
		{
			name:    "plan requires content",
			outcome: TerminalOutcome{Kind: TerminalOutcomePlan, Plan: &TerminalPlan{}},
			wantErr: "plan content is required",
		},
		{
			name: "questions reject plan payload",
			outcome: TerminalOutcome{
				Kind:      TerminalOutcomeQuestions,
				Plan:      &TerminalPlan{Content: "not a question"},
				Questions: []TerminalQuestion{{Text: "Continue?"}},
			},
			wantErr: "questions outcome must not carry a plan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.outcome.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}
