package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	rpchttp "github.com/flanksource/clicky/rpc/http"
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

	list, err := RunSessionList(context.Background(), SessionListOptions{Source: "claude", Limit: 10})
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

	detail, err := RunSessionGet(context.Background(), SessionGetOptions{ID: session.Key})
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

	current, err := RunSessionList(context.Background(), SessionListOptions{Source: "codex", Limit: 10})
	if err != nil {
		t.Fatalf("RunSessionList current: %v", err)
	}
	if current.Total != 1 || current.Sessions[0].ID != "codex-current" {
		t.Fatalf("current sessions = %+v", current)
	}

	all, err := RunSessionList(context.Background(), SessionListOptions{Source: "codex", All: true, Limit: 10})
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
	_, err := RunSessionGet(context.Background(), SessionGetOptions{ID: "missing"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestFindSessionCandidateByIDClaudeFilename(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	sessionFile := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "sess-fast.jsonl")
	if err := os.MkdirAll(filepath.Dir(sessionFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sessionFile, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}

	candidate, ok, err := findSessionCandidateByID("sess-fast", "all")
	if err != nil {
		t.Fatalf("findSessionCandidateByID: %v", err)
	}
	if !ok {
		t.Fatal("expected candidate")
	}
	if candidate.path != sessionFile {
		t.Fatalf("candidate.path = %q, want %q", candidate.path, sessionFile)
	}
	if candidate.record.ID != "sess-fast" || candidate.record.Source != "claude" {
		t.Fatalf("candidate.record = %+v", candidate.record)
	}
}

func TestRunSessionLiveEnrichesSummaryWithProcessHealth(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	writeJSONL(t, filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "sess-live.jsonl"),
		map[string]any{
			"type":      "user",
			"sessionId": "sess-live",
			"timestamp": "2026-06-01T10:00:00Z",
			"cwd":       project,
			"message": map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": "run something"}},
			},
		},
		map[string]any{
			"type":      "assistant",
			"sessionId": "sess-live",
			"timestamp": "2026-06-01T10:00:02Z",
			"cwd":       project,
			"message": map[string]any{
				"role":    "assistant",
				"model":   "claude-sonnet-4",
				"content": []any{map[string]any{"type": "text", "text": "working"}},
				"usage": map[string]any{
					"input_tokens":                930000,
					"cache_read_input_tokens":     10000,
					"cache_creation_input_tokens": 0,
					"output_tokens":               1000,
				},
			},
		},
	)

	started := time.Date(2026, 6, 1, 9, 59, 0, 0, time.UTC)
	orig := discoverSessionProcesses
	discoverSessionProcesses = func() ([]agentProcess, error) {
		return []agentProcess{{
			Source:        "claude",
			PID:           12345,
			Status:        "active",
			Active:        true,
			CPUPercent:    2.5,
			MemoryPercent: 1.25,
			StartedAt:     &started,
			CWD:           project,
			Command:       "claude",
		}}, nil
	}
	t.Cleanup(func() { discoverSessionProcesses = orig })

	result, err := RunSessionLive(context.Background(), SessionLiveOptions{Source: "claude", Limit: 10})
	if err != nil {
		t.Fatalf("RunSessionLive: %v", err)
	}
	if result.Total != 1 || len(result.Sessions) != 1 {
		t.Fatalf("live sessions = %+v", result)
	}
	session := result.Sessions[0]
	if session.Live == nil || session.Live.PID != 12345 {
		t.Fatalf("live state = %+v", session.Live)
	}
	if session.Context == nil || session.Context.FreePercent != 6 {
		t.Fatalf("context = %+v", session.Context)
	}
	if !hasHealth(session.Health, "low_context", "critical") {
		t.Fatalf("health = %+v", session.Health)
	}
	if result.Summary.ActiveSessions != 1 || result.Summary.AlertSessions != 1 {
		t.Fatalf("summary = %+v", result.Summary)
	}
	if result.Summary.LowestContextFree == nil || *result.Summary.LowestContextFree != 6 {
		t.Fatalf("lowest context = %+v", result.Summary.LowestContextFree)
	}
}

func TestRunSessionLiveScopesUnmatchedProcessesToCurrentProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	otherProject := filepath.Join(home, "work", "other")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherProject, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	orig := discoverSessionProcesses
	discoverSessionProcesses = func() ([]agentProcess, error) {
		return []agentProcess{
			{Source: "codex", PID: 100, Status: "sleeping", Active: true, CWD: project, Command: "codex"},
			{Source: "claude", PID: 200, Status: "sleeping", Active: true, CWD: otherProject, Command: "claude"},
		}, nil
	}
	t.Cleanup(func() { discoverSessionProcesses = orig })

	result, err := RunSessionLive(context.Background(), SessionLiveOptions{Source: "all", Limit: 10})
	if err != nil {
		t.Fatalf("RunSessionLive: %v", err)
	}
	if result.Total != 1 || len(result.Sessions) != 1 {
		t.Fatalf("live sessions = %+v", result)
	}
	if result.Sessions[0].Live == nil || result.Sessions[0].Live.PID != 100 {
		t.Fatalf("scoped live session = %+v", result.Sessions[0])
	}

	all, err := RunSessionLive(context.Background(), SessionLiveOptions{Source: "all", All: true, Limit: 10})
	if err != nil {
		t.Fatalf("RunSessionLive all: %v", err)
	}
	if all.Total != 2 {
		t.Fatalf("all project live sessions = %+v", all)
	}
}

func TestRunSessionLiveRecordsPhaseTimings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	writeJSONL(t, filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "sess-timing.jsonl"),
		map[string]any{
			"type":      "user",
			"sessionId": "sess-timing",
			"timestamp": "2026-06-01T10:00:00Z",
			"cwd":       project,
			"message": map[string]any{
				"role":    "user",
				"content": []any{map[string]any{"type": "text", "text": "hi"}},
			},
		},
	)

	orig := discoverSessionProcesses
	discoverSessionProcesses = func() ([]agentProcess, error) { return nil, nil }
	t.Cleanup(func() { discoverSessionProcesses = orig })

	ctx, timings := rpchttp.WithTimings(context.Background())
	if _, err := RunSessionLive(ctx, SessionLiveOptions{Source: "claude", Limit: 10}); err != nil {
		t.Fatalf("RunSessionLive: %v", err)
	}

	header := timings.Header()
	for _, phase := range []string{"find", "parse", "enrich"} {
		if !strings.Contains(header, phase+";dur=") {
			t.Fatalf("Header() = %q, missing phase %q", header, phase)
		}
	}
}

func hasHealth(signals []SessionHealthWire, kind, severity string) bool {
	for _, signal := range signals {
		if signal.Kind == kind && signal.Severity == severity {
			return true
		}
	}
	return false
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
