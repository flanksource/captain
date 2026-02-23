package claude

import (
	"strings"
	"testing"
)

func TestReadHistory(t *testing.T) {
	jsonl := `{"uuid":"1","sessionId":"s1","timestamp":"2024-01-15T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"Hello"}]}}
{"uuid":"2","sessionId":"s1","timestamp":"2024-01-15T10:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"Hi there!"}]}}
{"uuid":"3","sessionId":"s1","timestamp":"2024-01-15T10:02:00Z","message":{"role":"user","content":[{"type":"text","text":"Bye"}]}}`

	entries, err := ReadHistory(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ReadHistory failed: %v", err)
	}

	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}

	if entries[0].UUID != "1" || entries[0].Message.Role != MessageRoleUser {
		t.Errorf("unexpected first entry: %+v", entries[0])
	}

	if entries[1].UUID != "2" || entries[1].Message.Role != MessageRoleAssistant {
		t.Errorf("unexpected second entry: %+v", entries[1])
	}
}

func TestReadHistory_EmptyLines(t *testing.T) {
	jsonl := `{"uuid":"1","sessionId":"s1","timestamp":"2024-01-15T10:00:00Z","message":{"role":"user","content":[]}}

{"uuid":"2","sessionId":"s1","timestamp":"2024-01-15T10:01:00Z","message":{"role":"assistant","content":[]}}
`

	entries, err := ReadHistory(strings.NewReader(jsonl))
	if err != nil {
		t.Fatalf("ReadHistory failed: %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("expected 2 entries (skipping empty lines), got %d", len(entries))
	}
}

func TestReadHistory_InvalidJSON(t *testing.T) {
	jsonl := `{"uuid":"1","sessionId":"s1","timestamp":"2024-01-15T10:00:00Z","message":{"role":"user","content":[]}}
not valid json
{"uuid":"2","sessionId":"s1","timestamp":"2024-01-15T10:01:00Z","message":{"role":"assistant","content":[]}}`

	entries, err := ReadHistory(strings.NewReader(jsonl))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}

	if len(entries) != 1 {
		t.Errorf("expected 1 entry before error, got %d", len(entries))
	}
}

func TestReadStreamJSON(t *testing.T) {
	input := `{"type":"system","subtype":"init","cwd":"/tmp","session_id":"sess-1","model":"claude-sonnet-4-20250514","tools":["Bash","Read"]}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"list files"}]},"session_id":"sess-1","uuid":"msg-1"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"I'll list the files."},{"type":"tool_use","id":"tu-1","name":"Bash","input":{"command":"ls -la"}}]},"session_id":"sess-1","uuid":"msg-2","timestamp":"2024-01-15T10:00:00Z"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"tu-2","name":"Read","input":{"file_path":"/tmp/foo.go"}}]},"session_id":"sess-1","uuid":"msg-3","timestamp":"2024-01-15T10:01:00Z"}
{"type":"result","subtype":"success","session_id":"sess-1","cost_usd":0.01,"duration_ms":500}`

	entries, err := ReadStreamJSON(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ReadStreamJSON failed: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 assistant entries, got %d", len(entries))
	}

	if entries[0].UUID != "msg-2" {
		t.Errorf("expected UUID msg-2, got %s", entries[0].UUID)
	}
	if entries[0].SessionID != "sess-1" {
		t.Errorf("expected session sess-1, got %s", entries[0].SessionID)
	}
	if entries[0].Timestamp != "2024-01-15T10:00:00Z" {
		t.Errorf("expected timestamp 2024-01-15T10:00:00Z, got %s", entries[0].Timestamp)
	}

	toolUses := entries[0].Message.GetToolUses()
	if len(toolUses) != 1 || toolUses[0].Name != "Bash" {
		t.Errorf("expected Bash tool use in first entry, got %+v", toolUses)
	}

	toolUses = entries[1].Message.GetToolUses()
	if len(toolUses) != 1 || toolUses[0].Name != "Read" {
		t.Errorf("expected Read tool use in second entry, got %+v", toolUses)
	}
}

func TestReadStreamJSON_EmptyInput(t *testing.T) {
	entries, err := ReadStreamJSON(strings.NewReader(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
}

func TestHistoryIterator(t *testing.T) {
	jsonl := `{"uuid":"1","sessionId":"s1","timestamp":"2024-01-15T10:00:00Z","message":{"role":"user","content":[{"type":"text","text":"First"}]}}
{"uuid":"2","sessionId":"s1","timestamp":"2024-01-15T10:01:00Z","message":{"role":"assistant","content":[{"type":"text","text":"Second"}]}}`

	it := NewHistoryIterator(strings.NewReader(jsonl))

	var entries []HistoryEntry
	for it.Next() {
		entries = append(entries, it.Entry())
	}

	if it.Err() != nil {
		t.Fatalf("iterator error: %v", it.Err())
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	if entries[0].Message.GetTextContent() != "First" {
		t.Errorf("unexpected first text: %q", entries[0].Message.GetTextContent())
	}

	if entries[1].Message.GetTextContent() != "Second" {
		t.Errorf("unexpected second text: %q", entries[1].Message.GetTextContent())
	}
}

func TestHistoryIterator_Error(t *testing.T) {
	jsonl := `{"uuid":"1","sessionId":"s1","timestamp":"2024-01-15T10:00:00Z","message":{"role":"user","content":[]}}
invalid json here`

	it := NewHistoryIterator(strings.NewReader(jsonl))

	if !it.Next() {
		t.Fatal("expected first Next() to succeed")
	}

	if it.Next() {
		t.Error("expected second Next() to fail due to invalid JSON")
	}

	if it.Err() == nil {
		t.Error("expected Err() to return error")
	}
}
