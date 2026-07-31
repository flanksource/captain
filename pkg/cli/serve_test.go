package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/database"
)

func TestHandleThreadFromAgentCreatesThread(t *testing.T) {
	store := newFileThreadStore(filepath.Join(t.TempDir(), "threads.json"))
	body := `{"title":"Fix flaky test","providerSessionId":"sess-123","model":"codex-gpt-5-codex"}`
	req := httptest.NewRequest(http.MethodPost, "/api/captain/chat/threads/from-agent", strings.NewReader(body))
	rec := httptest.NewRecorder()

	handleThreadFromAgent(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		ID                string `json:"id"`
		Title             string `json:"title"`
		ProviderSessionID string `json:"providerSessionId"`
		LaunchURL         string `json:"launchUrl"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.ID == "" {
		t.Fatal("empty thread id")
	}
	if got.Title != "Fix flaky test" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.ProviderSessionID != "sess-123" {
		t.Errorf("ProviderSessionID = %q", got.ProviderSessionID)
	}
	if want := "/chat/" + got.ID + "?model=codex-gpt-5-codex"; got.LaunchURL != want {
		t.Errorf("LaunchURL = %q, want %q", got.LaunchURL, want)
	}
}

func TestHandleThreadFromAgentRequiresProviderSession(t *testing.T) {
	store := newFileThreadStore(filepath.Join(t.TempDir(), "threads.json"))
	req := httptest.NewRequest(http.MethodPost, "/api/captain/chat/threads/from-agent", strings.NewReader(`{"title":"missing"}`))
	rec := httptest.NewRecorder()

	handleThreadFromAgent(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "providerSessionId is required") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestHandleSessionGetReturnsAllMatches(t *testing.T) {
	home := t.TempDir()
	db := withTestCaptainDB(t)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)
	markProjectRoot(t, project)

	sessionFile := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "sess-web.jsonl")
	writeJSONL(t, sessionFile,
		map[string]any{
			"type": "assistant", "sessionId": "sess-web", "uuid": "a1",
			"timestamp": "2026-07-06T10:00:00Z", "cwd": project,
			"message": map[string]any{
				"role": "assistant", "model": "claude-opus-4-5",
				"content": []any{map[string]any{"type": "text", "text": "hello from the model"}},
			},
		},
	)
	for _, input := range []database.CreateSessionInput{
		{ProviderSessionID: "sess-web", Source: "claude", HostID: "test-host", Path: sessionFile, Project: "example", CWD: project},
		{ProviderSessionID: "sess-web", Source: "gavel", Provider: "cmux", HostID: "local"},
		{ProviderSessionID: "sess-web", Source: "claude", Provider: "cmux", HostID: "local"},
	} {
		if _, err := db.CreateOrGetSession(t.Context(), input); err != nil {
			t.Fatalf("create duplicate session: %v", err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/api/captain/sessions/sess-web", nil)
	req.SetPathValue("id", "sess-web")
	rec := httptest.NewRecorder()
	handleSessionGet()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got SessionGetResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, rec.Body.String())
	}
	if got.Total != 3 || len(got.Sessions) != 3 {
		t.Fatalf("result = %+v", got)
	}
	detail := got.Sessions[0].Detail
	if detail == nil || detail.ID != "sess-web" || detail.Source != "claude" {
		t.Fatalf("session = %+v", detail)
	}
	if len(detail.Messages) == 0 || detail.Messages[0].Parts[0].Text != "hello from the model" {
		t.Fatalf("messages = %+v", detail.Messages)
	}
}

func TestHandleProjectsReturnsOptions(t *testing.T) {
	home := t.TempDir()
	withTestCaptainDB(t)
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	markProjectRoot(t, project)
	writeJSONL(t, filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "sess-web.jsonl"),
		map[string]any{
			"type":      "assistant",
			"sessionId": "sess-web",
			"timestamp": "2026-07-06T10:00:00Z",
			"cwd":       project,
			"message": map[string]any{
				"role":    "assistant",
				"content": []any{map[string]any{"type": "text", "text": "hello"}},
			},
		},
	)

	req := httptest.NewRequest(http.MethodGet, "/api/captain/projects", nil)
	rec := httptest.NewRecorder()
	handleProjects()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got ProjectOptionsResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v (body=%s)", err, rec.Body.String())
	}
	if got.Total != 1 || got.Projects[0].Value != project {
		t.Fatalf("projects = %+v, want %q", got, project)
	}
}
