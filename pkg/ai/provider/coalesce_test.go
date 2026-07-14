package provider

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

// feedEvents pushes events onto a buffered channel and closes it, mimicking a
// finished stream for CoalesceStream to drain.
func feedEvents(events []ai.Event) <-chan ai.Event {
	ch := make(chan ai.Event, len(events))
	for _, ev := range events {
		ch <- ev
	}
	close(ch)
	return ch
}

func TestCoalesceStream_AccumulatesTextAndUsageFromResult(t *testing.T) {
	const model = "claude-3-5-sonnet-20241022"
	events := []ai.Event{
		{Kind: ai.EventSystem, Tool: "SessionInit", SessionID: "sess-1", Model: model},
		{Kind: ai.EventThinking, Text: "Need to inspect the file.", Model: model},
		{Kind: ai.EventText, Text: "I'll ", Model: model},
		{Kind: ai.EventText, Text: "read it.", Model: model},
		{Kind: ai.EventToolUse, Tool: "Read", Input: map[string]any{"file_path": "/repo/foo.go"}, Model: model},
		{Kind: ai.EventResult, Tool: "Result", Success: true, CostUSD: 0.0123, Model: model,
			Usage: &ai.Usage{InputTokens: 42, OutputTokens: 17, CacheReadTokens: 3, CacheWriteTokens: 1}},
	}

	resp, err := CoalesceStream(context.Background(), model, feedEvents(events), time.Now())
	if err != nil {
		t.Fatalf("CoalesceStream err: %v", err)
	}
	if resp.Text != "I'll read it." {
		t.Errorf("Text = %q, want %q", resp.Text, "I'll read it.")
	}
	if resp.Usage.InputTokens != 42 || resp.Usage.OutputTokens != 17 {
		t.Errorf("Usage = %+v, want input=42 output=17", resp.Usage)
	}
}

func TestCoalesceStream_CarriesStructuredData(t *testing.T) {
	const model = "gpt-5"
	structured := json.RawMessage(`{"answer":"42"}`)
	events := []ai.Event{
		{Kind: ai.EventText, Text: `{"answer":"42"}`, Model: model},
		{Kind: ai.EventResult, Tool: "Result", Success: true, Model: model, StructuredData: structured},
	}

	resp, err := CoalesceStream(context.Background(), model, feedEvents(events), time.Now())
	if err != nil {
		t.Fatalf("CoalesceStream err: %v", err)
	}
	raw, ok := resp.StructuredData.(json.RawMessage)
	if !ok {
		t.Fatalf("StructuredData = %T, want json.RawMessage", resp.StructuredData)
	}
	if string(raw) != string(structured) {
		t.Errorf("StructuredData = %s, want %s", raw, structured)
	}
}

func TestCoalesceStream_CarriesTerminalOutcome(t *testing.T) {
	events := []ai.Event{
		{Kind: ai.EventToolUse, Tool: "ExitPlanMode", Input: map[string]any{
			"plan":         "1. Inspect\n2. Implement",
			"planFilePath": "/repo/.claude/plans/example.md",
		}},
		{Kind: ai.EventResult, Success: true},
	}

	resp, err := CoalesceStream(context.Background(), "claude", feedEvents(events), time.Now())
	if err != nil {
		t.Fatalf("CoalesceStream err: %v", err)
	}
	if resp.TerminalOutcome == nil || resp.TerminalOutcome.Plan == nil {
		t.Fatalf("TerminalOutcome = %+v, want plan", resp.TerminalOutcome)
	}
	if resp.TerminalOutcome.Plan.Content != "1. Inspect\n2. Implement" {
		t.Errorf("plan content = %q", resp.TerminalOutcome.Plan.Content)
	}
}

func TestCoalesceStream_FailsOnMalformedTerminalOutcome(t *testing.T) {
	events := []ai.Event{
		{Kind: ai.EventToolUse, Tool: "ExitPlanMode", Input: map[string]any{"planFilePath": "/repo/plan.md"}},
		{Kind: ai.EventResult, Success: true},
	}

	resp, err := CoalesceStream(context.Background(), "claude", feedEvents(events), time.Now())
	if err == nil {
		t.Fatalf("CoalesceStream error = nil, response = %+v", resp)
	}
	if !strings.Contains(err.Error(), "plan is required") {
		t.Fatalf("CoalesceStream error = %v, want missing plan", err)
	}
}

func TestCoalesceStream_UsesResultTextWhenNoTextEvents(t *testing.T) {
	const model = "claude-sonnet-5"
	events := []ai.Event{
		{Kind: ai.EventResult, Tool: "Result", Success: true, Text: "final answer", Model: model},
	}

	resp, err := CoalesceStream(context.Background(), model, feedEvents(events), time.Now())
	if err != nil {
		t.Fatalf("CoalesceStream err: %v", err)
	}
	if resp.Text != "final answer" {
		t.Errorf("Text = %q, want final answer", resp.Text)
	}
}

func TestCoalesceStream_ResultErrorReturnsError(t *testing.T) {
	const wantMsg = "upstream 500"
	events := []ai.Event{
		{Kind: ai.EventResult, Tool: "Result", Success: false, Error: wantMsg, Model: "m"},
	}

	resp, err := CoalesceStream(context.Background(), "m", feedEvents(events), time.Now())
	if err == nil {
		t.Fatalf("expected error, got resp=%+v", resp)
	}
	if !strings.Contains(err.Error(), wantMsg) {
		t.Errorf("err = %v, want to mention %q", err, wantMsg)
	}
}
