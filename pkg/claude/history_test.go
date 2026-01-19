package claude

import (
	"encoding/json"
	"testing"
)

func TestHistoryEntry_Unmarshal(t *testing.T) {
	input := `{
		"parentUuid": "parent-123",
		"sessionId": "session-456",
		"version": "1.0.0",
		"uuid": "entry-789",
		"timestamp": "2024-01-15T10:30:00Z",
		"message": {
			"role": "assistant",
			"content": [
				{"type": "text", "text": "Hello!"},
				{"type": "tool_use", "id": "tool-1", "name": "Bash", "input": {"command": "ls"}}
			],
			"stop_reason": "tool_use",
			"usage": {
				"input_tokens": 100,
				"output_tokens": 50,
				"cache_read_input_tokens": 20
			}
		}
	}`

	var entry HistoryEntry
	if err := json.Unmarshal([]byte(input), &entry); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if entry.ParentUUID != "parent-123" {
		t.Errorf("expected parentUuid 'parent-123', got %q", entry.ParentUUID)
	}

	if entry.SessionID != "session-456" {
		t.Errorf("expected sessionId 'session-456', got %q", entry.SessionID)
	}

	if entry.Message.Role != MessageRoleAssistant {
		t.Errorf("expected role 'assistant', got %q", entry.Message.Role)
	}

	if len(entry.Message.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(entry.Message.Content))
	}

	if entry.Message.StopReason != StopReasonToolUse {
		t.Errorf("expected stop_reason 'tool_use', got %q", entry.Message.StopReason)
	}

	if entry.Message.Usage == nil || entry.Message.Usage.InputTokens != 100 {
		t.Errorf("unexpected usage: %+v", entry.Message.Usage)
	}
}

func TestHistoryEntry_IsUserMessage(t *testing.T) {
	entry := HistoryEntry{Message: Message{Role: MessageRoleUser}}
	if !entry.IsUserMessage() {
		t.Error("expected IsUserMessage() to return true")
	}
	if entry.IsAssistantMessage() {
		t.Error("expected IsAssistantMessage() to return false")
	}
}

func TestHistoryEntry_IsAssistantMessage(t *testing.T) {
	entry := HistoryEntry{Message: Message{Role: MessageRoleAssistant}}
	if !entry.IsAssistantMessage() {
		t.Error("expected IsAssistantMessage() to return true")
	}
	if entry.IsUserMessage() {
		t.Error("expected IsUserMessage() to return false")
	}
}

func TestMessage_GetTextContent(t *testing.T) {
	msg := Message{
		Content: []ContentBlock{
			{Type: ContentTypeText, Text: "Hello "},
			{Type: ContentTypeToolUse, Name: "Bash"},
			{Type: ContentTypeText, Text: "World"},
		},
	}

	text := msg.GetTextContent()
	if text != "Hello World" {
		t.Errorf("expected 'Hello World', got %q", text)
	}
}

func TestMessage_GetToolUses(t *testing.T) {
	msg := Message{
		Content: []ContentBlock{
			{Type: ContentTypeText, Text: "Hello"},
			{Type: ContentTypeToolUse, ID: "tool-1", Name: "Bash"},
			{Type: ContentTypeToolUse, ID: "tool-2", Name: "Read"},
			{Type: ContentTypeText, Text: "World"},
		},
	}

	uses := msg.GetToolUses()
	if len(uses) != 2 {
		t.Fatalf("expected 2 tool uses, got %d", len(uses))
	}

	if uses[0].Name != "Bash" || uses[1].Name != "Read" {
		t.Errorf("unexpected tool names: %v, %v", uses[0].Name, uses[1].Name)
	}
}

func TestMessage_GetToolResults(t *testing.T) {
	msg := Message{
		Content: []ContentBlock{
			{Type: ContentTypeToolResult, ToolUseID: "tool-1"},
			{Type: ContentTypeText, Text: "Done"},
			{Type: ContentTypeToolResult, ToolUseID: "tool-2", IsError: true},
		},
	}

	results := msg.GetToolResults()
	if len(results) != 2 {
		t.Fatalf("expected 2 tool results, got %d", len(results))
	}

	if results[0].ToolUseID != "tool-1" || results[1].IsError != true {
		t.Errorf("unexpected results: %+v", results)
	}
}

func TestHistoryEntry_ParseTimestamp(t *testing.T) {
	entry := HistoryEntry{Timestamp: "2024-01-15T10:30:00Z"}

	ts, err := entry.ParseTimestamp()
	if err != nil {
		t.Fatalf("ParseTimestamp failed: %v", err)
	}

	if ts.Year() != 2024 || ts.Month() != 1 || ts.Day() != 15 {
		t.Errorf("unexpected timestamp: %v", ts)
	}
}

func TestUsage_TotalTokens(t *testing.T) {
	usage := Usage{InputTokens: 100, OutputTokens: 50}
	if usage.TotalTokens() != 150 {
		t.Errorf("expected 150, got %d", usage.TotalTokens())
	}
}
