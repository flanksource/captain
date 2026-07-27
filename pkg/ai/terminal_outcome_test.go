package ai

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTerminalOutcomeFromEventPlan(t *testing.T) {
	outcome, err := TerminalOutcomeFromEvent(Event{
		Kind: EventToolUse,
		Tool: "ExitPlanMode",
		Input: map[string]any{
			"plan":         "1. Inspect the seam\n2. Implement it",
			"planFilePath": "/repo/.claude/plans/example.md",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, outcome)
	assert.Equal(t, TerminalOutcomePlan, outcome.Kind)
	require.NotNil(t, outcome.Plan)
	assert.Equal(t, "1. Inspect the seam\n2. Implement it", outcome.Plan.Content)
	assert.Equal(t, "/repo/.claude/plans/example.md", outcome.Plan.Path)
}

func TestTerminalOutcomeFromEventQuestions(t *testing.T) {
	outcome, err := TerminalOutcomeFromEvent(Event{
		Kind: EventToolUse,
		Tool: "AskUserQuestion",
		Input: map[string]any{"questions": []any{
			map[string]any{
				"question": "Which database?",
				"header":   "Storage",
				"options": []any{
					map[string]any{"label": "PostgreSQL", "description": "Production database"},
					"SQLite",
				},
			},
		}},
	})

	require.NoError(t, err)
	require.NotNil(t, outcome)
	assert.Equal(t, TerminalOutcomeQuestions, outcome.Kind)
	require.Equal(t, []TerminalQuestion{{
		Text:    "Which database?",
		Context: "Storage",
		Options: []string{"PostgreSQL", "SQLite"},
	}}, outcome.Questions)
}

func TestTerminalOutcomeFromEventFailsOnMalformedNativeOutcome(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  string
	}{
		{
			name:  "missing plan",
			event: Event{Kind: EventToolUse, Tool: "ExitPlanMode", Input: map[string]any{"planFilePath": "/repo/plan.md"}},
			want:  "plan is required",
		},
		{
			name: "invalid option",
			event: Event{Kind: EventToolUse, Tool: "AskUserQuestion", Input: map[string]any{
				"questions": []any{map[string]any{"question": "Continue?", "options": []any{42}}},
			}},
			want: "option 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			outcome, err := TerminalOutcomeFromEvent(tt.event)
			assert.Nil(t, outcome)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestTerminalOutcomeFromEventIgnoresOrdinaryTools(t *testing.T) {
	outcome, err := TerminalOutcomeFromEvent(Event{Kind: EventToolUse, Tool: "Read", Input: map[string]any{"file_path": "README.md"}})
	require.NoError(t, err)
	assert.Nil(t, outcome)
}

func TestPlanTerminalPermission(t *testing.T) {
	decision, handled := PlanTerminalPermission(true, PermissionRequest{Tool: "ExitPlanMode", Input: map[string]any{"plan": "1. do it"}})
	require.True(t, handled, "ExitPlanMode in plan mode is the terminal signal, not a brokered approval")
	assert.False(t, decision.Allow)
	assert.NotEmpty(t, decision.Message)

	_, handled = PlanTerminalPermission(false, PermissionRequest{Tool: "ExitPlanMode"})
	assert.False(t, handled, "outside plan mode ExitPlanMode brokers normally")

	_, handled = PlanTerminalPermission(true, PermissionRequest{Tool: "AskUserQuestion"})
	assert.False(t, handled, "AskUserQuestion keeps its interactive broker round-trip")
}
