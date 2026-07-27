package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseClaudeHookPayload(t *testing.T) {
	payload := `{
		"session_id": "abc123",
		"transcript_path": "/Users/x/.claude/projects/-repo/abc123.jsonl",
		"cwd": "/repo",
		"hook_event_name": "SessionStart",
		"source": "startup"
	}`
	ev, err := ParseClaudeHookPayload([]byte(payload))
	require.NoError(t, err)
	assert.Equal(t, HookEvent{
		Provider: "claude", Event: "SessionStart", SessionID: "abc123",
		TranscriptPath: "/Users/x/.claude/projects/-repo/abc123.jsonl",
		CWD:            "/repo", Detail: "startup",
	}, ev)

	end, err := ParseClaudeHookPayload([]byte(`{"session_id":"abc123","hook_event_name":"SessionEnd","reason":"logout"}`))
	require.NoError(t, err)
	assert.Equal(t, "logout", end.Detail)

	_, err = ParseClaudeHookPayload([]byte(`{"session_id":"abc123"}`))
	assert.Error(t, err, "payload without hook_event_name must be rejected")

	_, err = ParseClaudeHookPayload([]byte(`not json`))
	assert.Error(t, err)
}

func TestParseCodexNotifyPayload(t *testing.T) {
	// Payload shape from codex-rs hooks/legacy_notify.rs: kebab-case keys,
	// appended as the final argv argument.
	payload := `{"type":"agent-turn-complete","thread-id":"0195c1de-4ab8-7000-8000-0123456789ab",` +
		`"turn-id":"turn-1","cwd":"/repo","input-messages":["hi"],"last-assistant-message":"done"}`

	ev, err := ParseCodexNotifyPayload([]string{payload})
	require.NoError(t, err)
	assert.Equal(t, HookEvent{
		Provider: "codex", Event: "agent-turn-complete",
		SessionID: "0195c1de-4ab8-7000-8000-0123456789ab", CWD: "/repo", Detail: "turn-1",
	}, ev)

	_, err = ParseCodexNotifyPayload(nil)
	assert.Error(t, err, "missing payload argument must be rejected")

	_, err = ParseCodexNotifyPayload([]string{`{}`})
	assert.Error(t, err, "payload without type must be rejected")
}

func TestPostHookEvent(t *testing.T) {
	var gotPath string
	var gotEvent HookEvent
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&gotEvent))
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	ev := HookEvent{Provider: "claude", Event: "Stop", SessionID: "abc123", TranscriptPath: "/p.jsonl"}
	require.NoError(t, PostHookEvent(t.Context(), server.URL, ev))
	assert.Equal(t, "/api/captain/hooks/claude", gotPath)
	assert.Equal(t, ev, gotEvent)

	t.Run("non-2xx is an error", func(t *testing.T) {
		failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusServiceUnavailable)
		}))
		defer failing.Close()
		assert.Error(t, PostHookEvent(t.Context(), failing.URL, ev))
	})

	t.Run("unreachable server is an error", func(t *testing.T) {
		assert.Error(t, PostHookEvent(t.Context(), "http://127.0.0.1:1", ev))
	})
}
