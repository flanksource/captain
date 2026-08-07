package cli

import (
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/session"
	"github.com/segmentio/encoding/json"
)

type fakeTaskSink struct{}

func (fakeTaskSink) SetDescription(string)         {}
func (fakeTaskSink) SetProgress(int, int)          {}
func (fakeTaskSink) Infof(string, ...interface{})  {}
func (fakeTaskSink) Warnf(string, ...interface{})  {}
func (fakeTaskSink) Errorf(string, ...interface{}) {}

func collectEntries(model, backend string, events ...ai.Event) []session.Message {
	var out []session.Message
	acc := newPromptEventAccumulator(func(m session.Message) { out = append(out, m) }, fakeTaskSink{}, model, backend)
	for _, ev := range events {
		acc.handle(0, ev)
	}
	return out
}

// partText decodes a tool part's JSON-string Output back to plain text.
func partText(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}

func TestPromptRunAccumulator_CoalescesTextDeltas(t *testing.T) {
	msgs := collectEntries("m", "b",
		ai.Event{Kind: ai.EventText, Text: "Hello "},
		ai.Event{Kind: ai.EventText, Text: "world"},
	)
	if len(msgs) != 2 {
		t.Fatalf("want one frame per delta (2), got %d", len(msgs))
	}
	if msgs[0].ID != msgs[1].ID {
		t.Fatalf("text deltas must share an id: %q vs %q", msgs[0].ID, msgs[1].ID)
	}
	if got := msgs[1].Parts[0].Text; got != "Hello world" {
		t.Fatalf("coalesced text = %q, want %q", got, "Hello world")
	}
}

func TestPromptRunAccumulator_EmitsStructuredResultAsFreshMessage(t *testing.T) {
	msgs := collectEntries("m", "b",
		ai.Event{Kind: ai.EventText, Text: "narrative"},
		ai.Event{Kind: ai.EventResult, StructuredData: json.RawMessage(`{"answer":"42"}`)},
	)
	if len(msgs) != 2 {
		t.Fatalf("want narrative and one structured result frame, got %d", len(msgs))
	}
	if msgs[0].ID == msgs[1].ID {
		t.Fatalf("structured result must start a fresh message")
	}
	if got := msgs[1].Parts[0].Text; got != `{"answer":"42"}` {
		t.Fatalf("structured result text = %q, want coherent JSON", got)
	}
}

func TestPromptRunAccumulator_ThinkingAndTextAreSeparateTurns(t *testing.T) {
	msgs := collectEntries("m", "b",
		ai.Event{Kind: ai.EventThinking, Text: "hmm"},
		ai.Event{Kind: ai.EventText, Text: "answer"},
	)
	if len(msgs) != 2 {
		t.Fatalf("want 2 frames, got %d", len(msgs))
	}
	if msgs[0].ID == msgs[1].ID {
		t.Fatalf("thinking and text must not share an id")
	}
	if msgs[0].Parts[0].Type != session.PartReasoning {
		t.Fatalf("first frame part type = %q, want reasoning", msgs[0].Parts[0].Type)
	}
	if msgs[1].Parts[0].Type != session.PartText {
		t.Fatalf("second frame part type = %q, want text", msgs[1].Parts[0].Type)
	}
}

func TestPromptRunAccumulator_CorrelatesToolResult(t *testing.T) {
	msgs := collectEntries("m", "claude",
		ai.Event{Kind: ai.EventToolUse, Tool: "Bash", Input: map[string]any{"command": "ls"}, ToolCallID: "t1"},
		ai.Event{Kind: ai.EventToolResult, Text: "file.txt", Success: true, ToolCallID: "t1"},
	)
	if len(msgs) != 2 {
		t.Fatalf("want 2 frames (use + result), got %d", len(msgs))
	}
	for i, m := range msgs {
		if m.ID != "t1" {
			t.Fatalf("frame %d id = %q, want tool call id t1", i, m.ID)
		}
		if m.Parts[0].Type != session.PartTool {
			t.Fatalf("frame %d part type = %q, want a tool part", i, m.Parts[0].Type)
		}
	}
	if got := partText(msgs[1].Parts[0].Output); got != "file.txt" {
		t.Fatalf("tool output = %q, want file.txt", got)
	}
	if msgs[1].Parts[0].ToolName != "Bash" {
		t.Fatalf("result frame must carry original tool name, got %q", msgs[1].Parts[0].ToolName)
	}
	if msgs[1].Parts[0].State != session.ToolStateOutputAvailable {
		t.Fatalf("result state = %q, want output-available", msgs[1].Parts[0].State)
	}
}

func TestPromptRunAccumulator_MarksFailedToolResult(t *testing.T) {
	msgs := collectEntries("m", "claude",
		ai.Event{Kind: ai.EventToolUse, Tool: "Bash", ToolCallID: "t1"},
		ai.Event{Kind: ai.EventToolResult, Text: "boom", Success: false, ToolCallID: "t1"},
	)
	last := msgs[len(msgs)-1].Parts[0]
	if resp := partText(last.Output); !strings.HasPrefix(resp, "[error]") {
		t.Fatalf("failed tool output = %q, want [error] prefix", resp)
	}
	if last.State != session.ToolStateOutputError {
		t.Fatalf("failed tool state = %q, want output-error", last.State)
	}
}

func TestPromptRunAccumulator_EmitsErrorFrame(t *testing.T) {
	msgs := collectEntries("m", "b",
		ai.Event{Kind: ai.EventError, Error: "rate limited"},
	)
	if len(msgs) != 1 {
		t.Fatalf("want 1 error frame, got %d", len(msgs))
	}
	if got := msgs[0].Parts[0].Text; got != "rate limited" {
		t.Fatalf("error frame text = %q, want rate limited", got)
	}
}

// A hook narrating what it did between turns arrives as an EventSystem carrying
// text rather than a session id. It has to break out of the assistant's
// in-flight turn, not be appended to it, or a commit line lands inside the
// model's prose.
func TestPromptRunAccumulator_EmitsHookNoticeAsItsOwnSystemFrame(t *testing.T) {
	msgs := collectEntries("m", "b",
		ai.Event{Kind: ai.EventText, Text: "done, committing now"},
		ai.Event{Kind: ai.EventSystem, Text: "[post-turn] committed abc1234: fix: the thing"},
		ai.Event{Kind: ai.EventText, Text: "next turn"},
	)
	if len(msgs) != 3 {
		t.Fatalf("want assistant/system/assistant frames, got %d: %+v", len(msgs), msgs)
	}
	notice := msgs[1]
	if notice.Role != "system" {
		t.Errorf("notice role = %q, want system", notice.Role)
	}
	if got := notice.Parts[0].Text; got != "[post-turn] committed abc1234: fix: the thing" {
		t.Errorf("notice text = %q", got)
	}
	if msgs[2].ID == msgs[0].ID {
		t.Error("the turn after a notice reused the earlier text id, so a viewer that dedupes by id would overwrite it")
	}
	if msgs[2].Parts[0].Text != "next turn" {
		t.Errorf("text after a notice = %q, want a fresh buffer", msgs[2].Parts[0].Text)
	}
}

func TestPromptRunAccumulator_CapturesSessionAndUsage(t *testing.T) {
	acc := newPromptEventAccumulator(func(session.Message) {}, fakeTaskSink{}, "m", "b")
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
