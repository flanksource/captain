package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func requestCLIOptionsSchema(t *testing.T, backend string, wantStatus int) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/captain/ai/cli-options/catalog?backend="+backend, nil)
	handleCLIOptionsCatalog()(rec, req)
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d (body: %s)", rec.Code, wantStatus, rec.Body.String())
	}
	if wantStatus != http.StatusOK {
		return nil
	}
	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response not JSON: %v (body: %s)", err, rec.Body.String())
	}
	return doc
}

func TestHandleCLIOptionsCatalogCodex(t *testing.T) {
	doc := requestCLIOptionsSchema(t, "codex-cmux", http.StatusOK)
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties object: %#v", doc)
	}
	sandbox, ok := props["sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing sandbox property: %#v", props)
	}
	if _, ok := sandbox["enum"].([]any); !ok {
		t.Errorf("sandbox has no enum: %#v", sandbox)
	}
	if sandbox["title"] != "Sandbox" {
		t.Errorf("sandbox title = %v, want Sandbox", sandbox["title"])
	}
}

func TestHandleCLIOptionsCatalogClaude(t *testing.T) {
	doc := requestCLIOptionsSchema(t, "claude-cmux", http.StatusOK)
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties object: %#v", doc)
	}
	if _, ok := props["addDir"]; !ok {
		t.Errorf("claude schema missing addDir: %#v", props)
	}
	// Spec-owned flags must not leak into the extra-args form.
	if _, ok := props["permissionMode"]; ok {
		t.Errorf("claude schema must not expose spec-owned permissionMode")
	}
}

func TestHandleCLIOptionsCatalogUnknownBackend(t *testing.T) {
	requestCLIOptionsSchema(t, "anthropic", http.StatusBadRequest)
}
