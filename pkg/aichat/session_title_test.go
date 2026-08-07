package aichat

import (
	"encoding/json"
	"testing"

	"github.com/flanksource/captain/pkg/session"
)

func TestAgentTitleReadsTheLastNamingCall(t *testing.T) {
	input := func(title string) json.RawMessage {
		payload, err := json.Marshal(map[string]any{"aiTitle": title})
		if err != nil {
			t.Fatalf("marshal tool input: %v", err)
		}
		return payload
	}
	message := UIMessage{Role: "assistant", Parts: []UIPart{
		{Type: "text", Text: "working"},
		{Type: "dynamic-tool", ToolName: "invoice_get", ToolCallID: "call-1", Input: input("not a title")},
		{Type: "dynamic-tool", ToolName: session.TitleToolName, ToolCallID: "call-2", Input: input("First guess")},
		{Type: "dynamic-tool", ToolName: session.TitleToolName, ToolCallID: "call-3", Input: input("Account dimension backfill")},
	}}
	if got := agentTitle(message); got != "Account dimension backfill" {
		t.Fatalf("agentTitle = %q, want the last %s call's title", got, session.TitleToolName)
	}
	if got := agentTitle(UIMessage{Role: "assistant", Parts: []UIPart{{Type: "text", Text: "no tools here"}}}); got != "" {
		t.Fatalf("agentTitle = %q, want empty when the agent never named the thread", got)
	}
}

func TestDerivedTitleUsesTheFirstUserText(t *testing.T) {
	messages := []UIMessage{
		{Role: "assistant", Parts: []UIPart{{Type: "text", Text: "How can I help?"}}},
		{Role: "user", Parts: []UIPart{
			{Type: "file", Filename: "trial-balance.csv"},
			{Type: "text", Text: "  Reconcile\n the trial balance  "},
		}},
		{Role: "user", Parts: []UIPart{{Type: "text", Text: "and then post it"}}},
	}
	if got := derivedTitle(messages); got != "Reconcile the trial balance" {
		t.Fatalf("derivedTitle = %q, want the first user message collapsed", got)
	}
}

func TestTitleWinsFollowsNamingPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		current  string
		stored   TitleSource
		incoming TitleSource
		want     bool
	}{
		{name: "anything names a blank thread", current: "", stored: "", incoming: TitleSourceDerived, want: true},
		{name: "derived does not overwrite", current: "Named", stored: TitleSourceDerived, incoming: TitleSourceDerived, want: false},
		{name: "agent replaces derived", current: "Named", stored: TitleSourceDerived, incoming: TitleSourceAI, want: true},
		{name: "agent does not replace a person", current: "Named", stored: TitleSourceUser, incoming: TitleSourceAI, want: false},
		{name: "a person always wins", current: "Named", stored: TitleSourceAI, incoming: TitleSourceUser, want: true},
	} {
		if got := titleWins(tc.current, tc.stored, tc.incoming); got != tc.want {
			t.Errorf("%s: titleWins = %v, want %v", tc.name, got, tc.want)
		}
	}
}
