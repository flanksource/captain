package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/claude"
)

func TestRunSessionListAndGetClaude(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	sessionFile := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "sess-claude.jsonl")
	writeJSONL(t, sessionFile,
		map[string]any{
			"type":      "user",
			"sessionId": "sess-claude",
			"uuid":      "u1",
			"timestamp": "2026-06-01T10:00:00Z",
			"cwd":       project,
			"message": map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": "inspect this repo"}},
			},
		},
		map[string]any{
			"type":      "assistant",
			"sessionId": "sess-claude",
			"uuid":      "a1",
			"timestamp": "2026-06-01T10:00:01Z",
			"cwd":       project,
			"version":   "1.2.3",
			"gitBranch": "main",
			"message": map[string]any{
				"role":  "assistant",
				"model": "claude-sonnet-4",
				"content": []any{map[string]any{
					"type":  "tool_use",
					"id":    "tu-1",
					"name":  "Read",
					"input": map[string]any{"file_path": "README.md"},
				}},
			},
		},
		map[string]any{
			"type":      "user",
			"sessionId": "sess-claude",
			"uuid":      "r1",
			"timestamp": "2026-06-01T10:00:02Z",
			"cwd":       project,
			"message": map[string]any{
				"role": "user",
				"content": []any{map[string]any{
					"type":        "tool_result",
					"tool_use_id": "tu-1",
					"content":     "README contents",
				}},
			},
		},
	)

	list, err := RunSessionList(SessionListOptions{Source: "claude", Limit: 10})
	if err != nil {
		t.Fatalf("RunSessionList: %v", err)
	}
	if list.Total != 1 || len(list.Sessions) != 1 {
		t.Fatalf("sessions = %+v", list)
	}
	session := list.Sessions[0]
	if session.ID != "sess-claude" || session.Source != "claude" {
		t.Fatalf("session summary = %+v", session)
	}
	if session.ToolCalls != 1 || session.Messages != 1 {
		t.Fatalf("ToolCalls/Messages = %d/%d", session.ToolCalls, session.Messages)
	}
	if session.Key == "" {
		t.Fatal("empty session key")
	}

	detail, err := RunSessionGet(SessionGetOptions{ID: session.Key})
	if err != nil {
		t.Fatalf("RunSessionGet: %v", err)
	}
	if len(detail.Entries) != 2 {
		t.Fatalf("entries = %+v", detail.Entries)
	}
	if got := detail.Entries[0].Message.Content[0].Text; got != "inspect this repo" {
		t.Fatalf("first message text = %q", got)
	}
	if detail.Entries[1].ToolUse == nil {
		t.Fatalf("second entry should be tool use: %+v", detail.Entries[1])
	}
	if detail.Entries[1].ToolUse.Response != "README contents" {
		t.Fatalf("tool response = %q", detail.Entries[1].ToolUse.Response)
	}
}

func TestRunSessionListCodexScope(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	actualProject, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(actualProject), "other")

	writeCodexSession(t, filepath.Join(home, ".codex", "sessions", "2026", "06", "rollout-current.jsonl"), "codex-current", actualProject)
	writeCodexSession(t, filepath.Join(home, ".codex", "sessions", "2026", "06", "rollout-other.jsonl"), "codex-other", other)

	current, err := RunSessionList(SessionListOptions{Source: "codex", Limit: 10})
	if err != nil {
		t.Fatalf("RunSessionList current: %v", err)
	}
	if current.Total != 1 || current.Sessions[0].ID != "codex-current" {
		t.Fatalf("current sessions = %+v", current)
	}

	all, err := RunSessionList(SessionListOptions{Source: "codex", All: true, Limit: 10})
	if err != nil {
		t.Fatalf("RunSessionList all: %v", err)
	}
	if all.Total != 2 {
		t.Fatalf("all sessions = %+v", all)
	}
}

func TestRunSessionGetUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_, err := RunSessionGet(SessionGetOptions{ID: "missing"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

func writeCodexSession(t *testing.T, path, id, cwd string) {
	t.Helper()
	writeJSONL(t, path,
		map[string]any{
			"timestamp": "2026-06-01T10:00:00Z",
			"type":      "session_meta",
			"payload": map[string]any{
				"id":             id,
				"cwd":            cwd,
				"cli_version":    "0.1.0",
				"model_provider": "openai",
				"git":            map[string]any{"branch": "main"},
			},
		},
		map[string]any{
			"timestamp": "2026-06-01T10:00:01Z",
			"type":      "turn_context",
			"payload":   map[string]any{"model": "gpt-5", "effort": "medium"},
		},
		map[string]any{
			"timestamp": "2026-06-01T10:00:02Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":      "function_call",
				"name":      "shell",
				"arguments": `{"cmd":"ls"}`,
				"call_id":   "call-1",
			},
		},
		map[string]any{
			"timestamp": "2026-06-01T10:00:03Z",
			"type":      "response_item",
			"payload": map[string]any{
				"type":    "function_call_output",
				"call_id": "call-1",
				"output":  "ok",
			},
		},
	)
}

func writeJSONL(t *testing.T, path string, rows ...map[string]any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, row := range rows {
		data, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}
