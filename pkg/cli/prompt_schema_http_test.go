package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHandlePromptSchemaServesDocument(t *testing.T) {
	prev := schemaAdapters
	t.Cleanup(func() { schemaAdapters = prev })
	stub := stubbedSchemaAdapters(t)
	schemaAdapters = func() ([]AdapterStatus, error) { return stub, nil }

	req := httptest.NewRequest(http.MethodGet, "/api/captain/ai/prompt/schema", nil)
	rec := httptest.NewRecorder()
	handlePromptSchema()(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var doc map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if _, ok := doc["runtimeAdapters"].([]any); !ok {
		t.Fatalf("response has no runtimeAdapters[] array: %#v", doc["runtimeAdapters"])
	}
	if _, ok := doc["runtimes"].([]any); !ok {
		t.Fatalf("response has no runtimes[] catalog: %#v", doc["runtimes"])
	}
	if _, ok := doc["spec"].(map[string]any); !ok {
		t.Fatalf("response has no spec schema object")
	}
}
