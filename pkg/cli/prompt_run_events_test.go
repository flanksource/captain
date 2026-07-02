package cli

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
)

type fakeTaskSink struct{}

func (fakeTaskSink) SetDescription(string)         {}
func (fakeTaskSink) SetProgress(int, int)          {}
func (fakeTaskSink) Infof(string, ...interface{})  {}
func (fakeTaskSink) Warnf(string, ...interface{})  {}
func (fakeTaskSink) Errorf(string, ...interface{}) {}

func collectEntries(model, backend string, events ...ai.Event) []SessionEntryWire {
	var out []SessionEntryWire
	acc := newPromptEventAccumulator(func(e SessionEntryWire) { out = append(out, e) }, fakeTaskSink{}, model, backend)
	for _, ev := range events {
		acc.handle(0, ev)
	}
	return out
}

func TestPromptRunAccumulator_CoalescesTextDeltas(t *testing.T) {
	entries := collectEntries("m", "b",
		ai.Event{Kind: ai.EventText, Text: "Hello "},
		ai.Event{Kind: ai.EventText, Text: "world"},
	)
	if len(entries) != 2 {
		t.Fatalf("want one frame per delta (2), got %d", len(entries))
	}
	if entries[0].UUID != entries[1].UUID {
		t.Fatalf("text deltas must share a UUID: %q vs %q", entries[0].UUID, entries[1].UUID)
	}
	if got := entries[1].Message.Content[0].Text; got != "Hello world" {
		t.Fatalf("coalesced text = %q, want %q", got, "Hello world")
	}
}

func TestPromptRunAccumulator_ThinkingAndTextAreSeparateTurns(t *testing.T) {
	entries := collectEntries("m", "b",
		ai.Event{Kind: ai.EventThinking, Text: "hmm"},
		ai.Event{Kind: ai.EventText, Text: "answer"},
	)
	if len(entries) != 2 {
		t.Fatalf("want 2 frames, got %d", len(entries))
	}
	if entries[0].UUID == entries[1].UUID {
		t.Fatalf("thinking and text must not share a UUID")
	}
	if entries[0].Message.Content[0].Type != "thinking" {
		t.Fatalf("first frame type = %q, want thinking", entries[0].Message.Content[0].Type)
	}
	if entries[1].Message.Content[0].Type != "text" {
		t.Fatalf("second frame type = %q, want text", entries[1].Message.Content[0].Type)
	}
}

func TestPromptRunAccumulator_CorrelatesToolResult(t *testing.T) {
	entries := collectEntries("m", "claude",
		ai.Event{Kind: ai.EventToolUse, Tool: "Bash", Input: map[string]any{"command": "ls"}, ToolCallID: "t1"},
		ai.Event{Kind: ai.EventToolResult, Text: "file.txt", Success: true, ToolCallID: "t1"},
	)
	if len(entries) != 2 {
		t.Fatalf("want 2 frames (use + result), got %d", len(entries))
	}
	for i, e := range entries {
		if e.UUID != "t1" {
			t.Fatalf("frame %d UUID = %q, want tool call id t1", i, e.UUID)
		}
		if e.ToolUse == nil {
			t.Fatalf("frame %d missing tool_use", i)
		}
	}
	if got := entries[1].ToolUse.Response; got != "file.txt" {
		t.Fatalf("tool response = %q, want file.txt", got)
	}
	if entries[1].ToolUse.Tool != "Bash" {
		t.Fatalf("result frame must carry original tool name, got %q", entries[1].ToolUse.Tool)
	}
}

func TestPromptRunAccumulator_MarksFailedToolResult(t *testing.T) {
	entries := collectEntries("m", "claude",
		ai.Event{Kind: ai.EventToolUse, Tool: "Bash", ToolCallID: "t1"},
		ai.Event{Kind: ai.EventToolResult, Text: "boom", Success: false, ToolCallID: "t1"},
	)
	resp := entries[len(entries)-1].ToolUse.Response
	if !strings.HasPrefix(resp, "[error]") {
		t.Fatalf("failed tool response = %q, want [error] prefix", resp)
	}
}

func TestPromptRunAccumulator_EmitsErrorFrame(t *testing.T) {
	entries := collectEntries("m", "b",
		ai.Event{Kind: ai.EventError, Error: "rate limited"},
	)
	if len(entries) != 1 {
		t.Fatalf("want 1 error frame, got %d", len(entries))
	}
	if e := entries[0]; !e.IsAPIErrorMessage || e.Error != "rate limited" {
		t.Fatalf("error frame = %+v, want IsAPIErrorMessage + Error=rate limited", e)
	}
}

func TestPromptRunAccumulator_CapturesSessionAndUsage(t *testing.T) {
	acc := newPromptEventAccumulator(func(SessionEntryWire) {}, fakeTaskSink{}, "m", "b")
	acc.handle(0, ai.Event{Kind: ai.EventSystem, SessionID: "sess-1"})
	acc.handle(0, ai.Event{Kind: ai.EventResult, Usage: &ai.Usage{InputTokens: 10, OutputTokens: 4}, CostUSD: 0.02})
	if acc.sessionID != "sess-1" {
		t.Fatalf("sessionID = %q, want sess-1", acc.sessionID)
	}
	if acc.usage.InputTokens != 10 || acc.usage.OutputTokens != 4 {
		t.Fatalf("usage = %+v, want 10/4", acc.usage)
	}
	if acc.cost != 0.02 {
		t.Fatalf("cost = %v, want 0.02", acc.cost)
	}
}
