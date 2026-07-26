package session

import (
	"testing"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/segmentio/encoding/json"
)

// TestToUIMessages_MergesToolResultIntoCall verifies the chat projection folds a
// tool_result into its originating tool call (matched by ToolCallID) and drops
// the standalone result part, and that metadata carries the session cost.
func TestToUIMessages_MergesToolResultIntoCall(t *testing.T) {
	readInput := rawInput(t, map[string]any{"file_path": "/repo/x.go"})
	e := assistantEntry("a1", "", "claude-opus-4-5",
		&claude.Usage{InputTokens: 1000, OutputTokens: 500},
		claude.ContentBlock{Type: claude.ContentTypeText, Text: "reading"},
		toolUseBlock("tu-1", "Read", readInput),
	)
	result := claude.HistoryEntry{
		UUID: "u2", SessionID: "root-sess", Timestamp: "2026-07-05T10:00:01Z",
		Message: claude.Message{
			Role: claude.MessageRoleUser,
			Content: []claude.ContentBlock{{
				Type:      claude.ContentTypeToolResult,
				ToolUseID: "tu-1",
				Content:   json.RawMessage(`"file body"`),
			}},
		},
	}
	ps := claude.ParsedSession{
		SessionID:   "root-sess",
		Transcripts: []claude.ParsedTranscript{{Path: "/p/root-sess.jsonl", Entries: []claude.HistoryEntry{e, result}}},
	}

	s := buildSession(ps)
	msgs, meta := s.ToUIMessages()

	// The user tool_result message had only a result part → merged and dropped.
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1 (result merged into the call)", len(msgs))
	}
	var toolPart *Part
	for i := range msgs[0].Parts {
		if isToolCall(msgs[0].Parts[i]) {
			toolPart = &msgs[0].Parts[i]
		}
	}
	if toolPart == nil {
		t.Fatal("no tool-call part found")
	}
	if toolPart.State != ToolStateOutputAvailable {
		t.Errorf("tool state = %q, want %q after merge", toolPart.State, ToolStateOutputAvailable)
	}
	if string(toolPart.Output) != `"file body"` {
		t.Errorf("tool output = %s, want the merged result body", toolPart.Output)
	}
	if meta.Cost == 0 || meta.CostBreakdown == nil {
		t.Errorf("metadata cost not populated: %+v", meta)
	}
}

// TestToReplayEntries_MessageAndToolRows verifies the replay projection emits a
// message entry for text/thinking and a separate tool entry carrying the merged
// result as Response — the shape the session viewer / clicky-ui consume.
func TestToReplayEntries_MessageAndToolRows(t *testing.T) {
	readInput := rawInput(t, map[string]any{"file_path": "/repo/x.go"})
	e := assistantEntry("a1", "", "claude-opus-4-5", nil,
		claude.ContentBlock{Type: claude.ContentTypeText, Text: "reading the file"},
		toolUseBlock("tu-1", "Read", readInput),
	)
	result := claude.HistoryEntry{
		UUID: "u2", SessionID: "root-sess", Timestamp: "2026-07-05T10:00:01Z",
		Message: claude.Message{
			Role: claude.MessageRoleUser,
			Content: []claude.ContentBlock{{
				Type: claude.ContentTypeToolResult, ToolUseID: "tu-1", Content: json.RawMessage(`"file body"`),
			}},
		},
	}
	s := buildSession(claude.ParsedSession{
		SessionID:   "root-sess",
		Transcripts: []claude.ParsedTranscript{{Path: "/p/root-sess.jsonl", Entries: []claude.HistoryEntry{e, result}}},
	})

	entries := s.ToReplayEntries()
	// one message entry (text) + one tool entry.
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2 (message + tool)", len(entries))
	}
	if entries[0].Message == nil || entries[0].Message.Content[0].Text != "reading the file" {
		t.Errorf("first entry should be the assistant text message, got %+v", entries[0])
	}
	tool := entries[1].ToolUse
	if tool == nil || tool.Tool != "Read" || tool.ToolUseID != "tu-1" {
		t.Fatalf("second entry should be the Read tool, got %+v", entries[1])
	}
	if tool.Source != "claude" {
		t.Errorf("tool source = %q, want claude", tool.Source)
	}
	if tool.Response != "file body" {
		t.Errorf("tool response = %q, want merged 'file body'", tool.Response)
	}
	if tool.Input["file_path"] != "/repo/x.go" {
		t.Errorf("tool input = %v, want file_path /repo/x.go", tool.Input)
	}
}
