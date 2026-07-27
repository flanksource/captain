package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flanksource/captain/pkg/monitor"
	"github.com/stretchr/testify/assert"
)

func postHookEventRequest(t *testing.T, provider, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/captain/hooks/{provider}", handleMonitorHookEvent())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/captain/hooks/"+provider, strings.NewReader(body))
	mux.ServeHTTP(rec, req)
	return rec
}

func TestHandleMonitorHookEvent(t *testing.T) {
	validBody := `{"event":"Stop","sessionId":"abc123","transcriptPath":"/p.jsonl"}`

	t.Run("unknown provider is rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postHookEventRequest(t, "gemini", validBody).Code)
	})

	t.Run("malformed body is rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postHookEventRequest(t, "claude", "not json").Code)
	})

	t.Run("missing event name is rejected", func(t *testing.T) {
		assert.Equal(t, http.StatusBadRequest, postHookEventRequest(t, "claude", `{"sessionId":"abc123"}`).Code)
	})

	t.Run("no running monitor is unavailable", func(t *testing.T) {
		setServeMonitor(nil)
		assert.Equal(t, http.StatusServiceUnavailable, postHookEventRequest(t, "claude", validBody).Code)
	})

	t.Run("valid event is accepted", func(t *testing.T) {
		setServeMonitor(&monitor.Monitor{})
		t.Cleanup(func() { setServeMonitor(nil) })
		assert.Equal(t, http.StatusAccepted, postHookEventRequest(t, "claude", validBody).Code)
	})
}
