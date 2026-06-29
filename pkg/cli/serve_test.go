package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
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
