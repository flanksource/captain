package database

import (
	"testing"
)

func TestResolvePromptRunRuntimeSelectionPerField(t *testing.T) {
	t.Parallel()

	resolved := PromptRunRuntimeSelection{Provider: "openai", Model: "gpt-resolved"}
	requested := PromptRunRuntimeSelection{Provider: "openai", Mode: "agent", Model: "gpt-requested", Effort: "high"}
	execution := PromptRunRuntimeSelection{Provider: "anthropic", Mode: "agent", Model: "claude-session", Effort: "medium"}

	actual := resolvePromptRunRuntimeSelection(resolved, requested, execution)
	want := PromptRunRuntimeSelection{
		Provider: "openai",
		Mode:     "agent",
		Model:    "gpt-resolved",
		Effort:   "high",
	}
	if actual != want {
		t.Fatalf("resolved runtime = %#v, want %#v", actual, want)
	}
}

func TestPromptRunDisplayStatusPrecedence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		state    PromptRunState
		mode     string
		activity string
		health   string
		want     PromptRunDisplayStatus
	}{
		{name: "completed beats stale process health", state: PromptRunStateSucceeded, health: string(SessionHealthZombie), want: PromptRunStatusCompleted},
		{name: "failed", state: PromptRunStateFailed, want: PromptRunStatusFailed},
		{name: "cancelled", state: PromptRunStateCancelled, want: PromptRunStatusCancelled},
		{name: "zombie beats ask", state: PromptRunStateWaiting, activity: string(SessionActivityAsk), health: string(SessionHealthZombie), want: PromptRunStatusZombie},
		{name: "waiting means ask", state: PromptRunStateWaiting, want: PromptRunStatusAsk},
		{name: "session ask activity", state: PromptRunStateRunning, activity: string(SessionActivityAsk), want: PromptRunStatusAsk},
		{name: "plan run", state: PromptRunStateRunning, mode: "plan", want: PromptRunStatusPlanning},
		{name: "pending run", state: PromptRunStatePending, mode: "run", want: PromptRunStatusRunning},
		{name: "running", state: PromptRunStateRunning, want: PromptRunStatusRunning},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := promptRunDisplayStatus(tt.state, tt.mode, tt.activity, tt.health)
			if err != nil {
				t.Fatalf("promptRunDisplayStatus: %v", err)
			}
			if actual != tt.want {
				t.Fatalf("status = %q, want %q", actual, tt.want)
			}
		})
	}
}

func TestPromptRunDisplayStatusRejectsUnknownState(t *testing.T) {
	t.Parallel()

	if _, err := promptRunDisplayStatus(PromptRunState("unknown"), "", "", ""); err == nil {
		t.Fatal("expected an unknown prompt-run state to fail")
	}
}
