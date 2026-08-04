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
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/monitor"
)

func TestRunSessionListAndGetClaude(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
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
	if session.ToolCalls != 1 || session.Messages != 3 {
		t.Fatalf("ToolCalls/Messages = %d/%d", session.ToolCalls, session.Messages)
	}
	if session.Key == "" {
		t.Fatal("empty session key")
	}

	detail, err := RunSessionGet(context.Background(), SessionGetOptions{ID: session.Key})
	if err != nil {
		t.Fatalf("RunSessionGet: %v", err)
	}
	if detail.Total != 1 || len(detail.Sessions) != 1 || detail.Sessions[0].Detail == nil {
		t.Fatalf("session detail result = %+v", detail)
	}
	item := detail.Sessions[0]
	parsed := item.Detail
	if parsed.Source != "claude" || parsed.ID != item.CaptainID || parsed.ProviderSessionID != "sess-claude" {
		t.Fatalf("session detail meta = %+v", parsed)
	}
	entries := parsed.ToReplayEntries()
	if len(entries) != 2 {
		t.Fatalf("entries = %+v", entries)
	}
	if got := entries[0].Message.Content[0].Text; got != "inspect this repo" {
		t.Fatalf("first message text = %q", got)
	}
	if entries[1].ToolUse == nil {
		t.Fatalf("second entry should be tool use: %+v", entries[1])
	}
	if entries[1].ToolUse.Response != "README contents" {
		t.Fatalf("tool response = %q", entries[1].ToolUse.Response)
	}
}

func TestRunSessionListCodexScope(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
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
	markProjectRoot(t, actualProject)
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

func TestSessionMatchesQueryIncludesIdentity(t *testing.T) {
	record := SessionRecord{
		ID:            "session-1",
		Source:        "codex",
		Project:       "captain",
		Title:         "Improve session identity",
		InitialPrompt: "Show the initial user request in the listing",
	}
	for _, query := range []string{"captain", "identity", "initial user request"} {
		if !sessionMatchesQuery(record, query) {
			t.Fatalf("query %q did not match %+v", query, record)
		}
	}
}

func TestRunSessionGetUnknown(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
	t.Setenv("HOME", home)
	_, err := RunSessionGet(context.Background(), SessionGetOptions{ID: "missing"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestRunSessionGetReturnsAllProviderPrefixMatches(t *testing.T) {
	home := t.TempDir()
	db := withTestCaptainDB(t)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	providerID := "ad4c854e-cde6-4b99-99f3-667bf74112e3"
	sessionFile := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), providerID+".jsonl")
	writeJSONL(t, sessionFile,
		map[string]any{
			"type": "assistant", "sessionId": providerID, "uuid": "a1",
			"timestamp": "2026-07-14T10:00:00Z", "cwd": project,
			"message": map[string]any{
				"role": "assistant", "model": "claude-opus-4-5",
				"content": []any{map[string]any{"type": "text", "text": "canonical transcript"}},
			},
		},
	)
	for _, input := range []database.CreateSessionInput{
		{ProviderSessionID: providerID, Source: "claude", HostID: "test-host", Path: sessionFile, Project: "flanksource", CWD: project},
		{ProviderSessionID: providerID, Source: "gavel", Provider: "cmux", HostID: "local", Project: "xero-cli"},
		{ProviderSessionID: providerID, Source: "claude", Provider: "cmux", HostID: "local", Project: "xero-cli"},
	} {
		if _, err := db.CreateOrGetSession(t.Context(), input); err != nil {
			t.Fatalf("create duplicate session: %v", err)
		}
	}

	result, err := RunSessionGet(t.Context(), SessionGetOptions{ID: "ad4c854e"})
	if err != nil {
		t.Fatalf("RunSessionGet: %v", err)
	}
	if result.Total != 3 || len(result.Sessions) != 3 {
		t.Fatalf("result = %+v", result)
	}
	if !result.Sessions[0].DetailAvailable || result.Sessions[0].Detail == nil {
		t.Fatalf("first match should contain parsed transcript: %+v", result.Sessions[0])
	}
	for _, item := range result.Sessions[1:] {
		if item.DetailAvailable || item.Detail != nil {
			t.Fatalf("metadata-only match = %+v", item)
		}
	}
}

func TestRunSessionLiveEnrichesSummaryWithProcessHealth(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
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
	monitorDiscoverProcesses = func() ([]monitor.Process, error) {
		return []monitor.Process{{
			Source:        "claude",
			PID:           12345,
			Status:        "active",
			CPUPercent:    2.5,
			MemoryPercent: 1.25,
			StartedAt:     &started,
			CWD:           project,
			Command:       "claude",
		}}, nil
	}
	refreshTestSessionDB(t)

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

func TestRunSessionLiveExcludesEndedSessions(t *testing.T) {
	home := t.TempDir()
	db := withTestCaptainDB(t)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	endedCWD := filepath.Join(project, "ended")
	if err := os.MkdirAll(endedCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	live, err := db.CreateOrGetSession(t.Context(), database.CreateSessionInput{
		ProviderSessionID: "sess-live", Source: "claude", CWD: project,
	})
	if err != nil {
		t.Fatalf("create live session: %v", err)
	}
	ended, err := db.CreateOrGetSession(t.Context(), database.CreateSessionInput{
		ProviderSessionID: "sess-ended", Source: "claude", CWD: endedCWD,
	})
	if err != nil {
		t.Fatalf("create ended session: %v", err)
	}

	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	hostID := captainHostID()
	for _, process := range []database.SessionProcessInput{
		{SessionID: live.ID, HostID: hostID, BootID: "boot", PID: 12345, ProcessStartedAt: started, Status: "active", CWD: project, Source: "claude"},
		{SessionID: ended.ID, HostID: hostID, BootID: "boot", PID: 54321, ProcessStartedAt: started, Status: "active", CWD: endedCWD, Source: "claude"},
	} {
		if err := db.UpsertSessionProcess(t.Context(), process); err != nil {
			t.Fatalf("upsert process: %v", err)
		}
	}
	if _, err := db.EndVanishedProcesses(t.Context(), hostID, []int64{12345}); err != nil {
		t.Fatalf("end historical process: %v", err)
	}
	monitorDiscoverProcesses = func() ([]monitor.Process, error) {
		return []monitor.Process{{
			Source: "claude", PID: 12345, Status: "active", StartedAt: &started,
			CWD: project, Command: "claude",
		}}, nil
	}

	result, err := RunSessionLive(context.Background(), SessionLiveOptions{Source: "all", All: true, Limit: 10})
	if err != nil {
		t.Fatalf("RunSessionLive: %v", err)
	}
	if result.Total != 1 || len(result.Sessions) != 1 || result.Sessions[0].ID != "sess-live" {
		t.Fatalf("live sessions = %+v", result)
	}
}

func TestRunSessionLiveScopesUnmatchedProcessesToCurrentProject(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
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
	markProjectRoot(t, project)

	monitorDiscoverProcesses = func() ([]monitor.Process, error) {
		return []monitor.Process{
			{Source: "codex", PID: 100, Status: "sleeping", CWD: project, Command: "codex"},
			{Source: "claude", PID: 200, Status: "sleeping", CWD: otherProject, Command: "claude"},
		}, nil
	}
	refreshTestSessionDB(t)

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

func TestRunSessionLiveRestrictsExplicitProject(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
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
	markProjectRoot(t, project)
	markProjectRoot(t, otherProject)

	writeJSONL(t, filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "sess-current.jsonl"),
		map[string]any{
			"type":      "assistant",
			"sessionId": "sess-current",
			"timestamp": "2026-06-01T10:00:00Z",
			"cwd":       project,
			"message": map[string]any{
				"role":    "assistant",
				"model":   "claude-sonnet-4",
				"content": []any{map[string]any{"type": "text", "text": "current"}},
			},
		},
	)
	writeJSONL(t, filepath.Join(home, ".claude", "projects", claude.NormalizePath(otherProject), "sess-other.jsonl"),
		map[string]any{
			"type":      "assistant",
			"sessionId": "sess-other",
			"timestamp": "2026-06-01T10:00:01Z",
			"cwd":       otherProject,
			"message": map[string]any{
				"role":    "assistant",
				"model":   "claude-sonnet-4",
				"content": []any{map[string]any{"type": "text", "text": "other"}},
			},
		},
	)
	monitorDiscoverProcesses = func() ([]monitor.Process, error) {
		return []monitor.Process{
			{Source: "claude", PID: 301, Status: "sleeping", CWD: project, Command: "claude --resume sess-current"},
			{Source: "claude", PID: 302, Status: "sleeping", CWD: otherProject, Command: "claude --resume sess-other"},
		}, nil
	}
	refreshTestSessionDB(t)

	result, err := RunSessionLive(context.Background(), SessionLiveOptions{
		Source:  "claude",
		All:     true,
		Project: otherProject,
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("RunSessionLive: %v", err)
	}
	if result.Scope != "project" || result.Project != otherProject {
		t.Fatalf("scope/project = %q/%q, want project/%q", result.Scope, result.Project, otherProject)
	}
	if result.Total != 1 || len(result.Sessions) != 1 || result.Sessions[0].ID != "sess-other" {
		t.Fatalf("project-scoped sessions = %+v", result)
	}
}

// markProjectRoot writes a project marker in dir so claude.FindProjectInfo
// resolves dir as the project root deterministically. Without it, a temp project
// dir has no marker and FindProjectInfo walks to the filesystem root — under
// `go test ./...` a concurrent package's test can leave a go.mod/.git in the
// shared temp root, resolving matchRoot above the project and letting
// sibling-project sessions leak into the current scope.
func markProjectRoot(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644); err != nil {
		t.Fatal(err)
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
