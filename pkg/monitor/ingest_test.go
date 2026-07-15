package monitor

import (
	"testing"

	"github.com/flanksource/captain/pkg/session"
)

func TestUnifiedIngestInputPreservesTurnEffort(t *testing.T) {
	input := unifiedIngestInput(&session.Session{Turns: []session.Turn{{
		ID: "turn-1", Index: 1, Model: "gpt-5.6-sol", ReasoningEffort: "max",
	}}}, "codex", func(session.Message, int) int64 { return 0 })

	if len(input.Turns) != 1 || input.Turns[0].Call == nil {
		t.Fatalf("turns = %+v, want one model call", input.Turns)
	}
	if input.Turns[0].Call.Effort != "max" {
		t.Fatalf("effort = %q, want max", input.Turns[0].Call.Effort)
	}
}

func TestUnifiedIngestInputPreservesAgentTurnOwnership(t *testing.T) {
	input := unifiedIngestInput(&session.Session{Turns: []session.Turn{{
		ID: "child/turn-1", AgentID: "child", Index: 1, Model: "claude-haiku-4-5",
	}}}, "claude", func(session.Message, int) int64 { return 0 })

	if len(input.Turns) != 1 || input.Turns[0].ProviderTurnID != "child/turn-1" {
		t.Fatalf("turns = %+v, want the child agent turn", input.Turns)
	}
}
