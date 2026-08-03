package anthropicmock

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/aimock"
)

func TestHeldStreamRecordsCancellationWithoutAMiss(t *testing.T) {
	srv := startServer(t, "hold-open.yaml")
	raw, err := json.Marshal(map[string]any{
		"model": "claude-sonnet-5", "stream": true,
		"messages": []any{userTurn("wait for interruption")},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL()+"/v1/messages", bytes.NewReader(raw))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	cancel()
	_, _ = io.ReadAll(response.Body)
	_ = response.Body.Close()
	require.Eventually(t, func() bool { return len(srv.Requests()) == 1 }, time.Second, 10*time.Millisecond)
	assert.True(t, srv.Requests()[0].Cancelled)
	assert.Empty(t, srv.Requests()[0].Miss)
}

const scenarioDir = "../testdata/scenarios"

func startServer(t *testing.T, scenarioFile string, opts ...func(*Options)) *Server {
	t.Helper()
	scenario, err := aimock.Load(filepath.Join(scenarioDir, scenarioFile))
	require.NoError(t, err)

	options := Options{Scenario: scenario}
	for _, apply := range opts {
		apply(&options)
	}
	srv, err := Start(options)
	require.NoError(t, err)
	t.Cleanup(srv.Close)
	return srv
}

// post sends a Messages API request and returns the raw response for the caller
// to decode as JSON or as a stream.
func post(t *testing.T, srv *Server, body map[string]any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	resp, err := http.Post(srv.URL()+"/v1/messages", "application/json", bytes.NewReader(raw))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func userTurn(text string) map[string]any {
	return map[string]any{"role": "user", "content": text}
}

func TestNonStreamingTextReply(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp := post(t, srv, map[string]any{
		"model":    "claude-sonnet-5",
		"messages": []any{userTurn("What is the capital of France?")},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got messageResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))

	assert.Equal(t, "message", got.Type)
	assert.Equal(t, "assistant", got.Role)
	assert.Equal(t, "claude-sonnet-5", got.Model)
	assert.Equal(t, StopEndTurn, got.StopReason)
	assert.Equal(t, wireUsage{InputTokens: 14, OutputTokens: 8}, got.Usage)
	require.Len(t, got.Content, 1)
	assert.Equal(t, "text", got.Content[0].Type)
	assert.Equal(t, "The capital of France is Paris.", got.Content[0].Text)

	assert.Empty(t, srv.Remaining(), "the scenario should have played out fully")
}

func TestNonStreamingToolUseReply(t *testing.T) {
	srv := startServer(t, "bash-tool.yaml")

	resp := post(t, srv, map[string]any{
		"model":    "claude-sonnet-5",
		"messages": []any{userTurn("please list the files")},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got messageResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))

	assert.Equal(t, StopToolUse, got.StopReason)
	require.Len(t, got.Content, 2, "thinking then tool_use, in wire order")
	assert.Equal(t, "thinking", got.Content[0].Type)
	assert.NotEmpty(t, got.Content[0].Signature, "a thinking block always carries a signature")

	assert.Equal(t, "tool_use", got.Content[1].Type)
	assert.Equal(t, "Bash", got.Content[1].Name)
	assert.Equal(t, "toolu_mock_bash", got.Content[1].ID)
	assert.JSONEq(t, `{"command":"ls -1","description":"List files in the working directory"}`, string(got.Content[1].Input))
}

// The second turn of the loop carries the tool result, and must consume the
// second rule rather than re-firing the first.
func TestToolResultConsumesTheNextRule(t *testing.T) {
	srv := startServer(t, "bash-tool.yaml")

	first := post(t, srv, map[string]any{
		"model":    "claude-sonnet-5",
		"messages": []any{userTurn("please list the files")},
	})
	require.Equal(t, http.StatusOK, first.StatusCode)

	second := post(t, srv, map[string]any{
		"model": "claude-sonnet-5",
		"messages": []any{
			userTurn("please list the files"),
			map[string]any{"role": "assistant", "content": []any{
				map[string]any{"type": "tool_use", "id": "toolu_mock_bash", "name": "Bash", "input": map[string]any{}},
			}},
			map[string]any{"role": "user", "content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": "toolu_mock_bash", "content": "README.md\nmain.go"},
			}},
		},
	})
	require.Equal(t, http.StatusOK, second.StatusCode)

	var got messageResponse
	require.NoError(t, json.NewDecoder(second.Body).Decode(&got))
	assert.Equal(t, StopEndTurn, got.StopReason)
	require.Len(t, got.Content, 1)
	assert.Equal(t, "The directory contains README.md and main.go.", got.Content[0].Text)

	assert.Empty(t, srv.Remaining())
	assert.Len(t, srv.Requests(), 2)
}

func TestStreamingTextEventSequence(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp := post(t, srv, map[string]any{
		"model":    "claude-sonnet-5",
		"stream":   true,
		"messages": []any{userTurn("What is the capital of France?")},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	frames, err := aimock.ReadSSE(resp.Body)
	require.NoError(t, err)

	names := aimock.EventNames(frames)
	assert.Equal(t, []string{
		"message_start", "ping",
		"content_block_start",
		"content_block_delta", "content_block_delta", "content_block_delta",
		"content_block_delta", "content_block_delta", "content_block_delta",
		"content_block_stop",
		"message_delta", "message_stop",
	}, names)

	// Every frame's payload names its own type, which is what consumers switch on.
	for i, frame := range frames {
		assert.Equal(t, frame.Event, frame.Type(), "frame %d payload type must match its event name", i)
	}

	assert.Equal(t, "The capital of France is Paris.", collectText(t, frames))
}

func TestStreamingMessageStartDefersOutputTokens(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp := post(t, srv, map[string]any{
		"model":    "claude-sonnet-5",
		"stream":   true,
		"messages": []any{userTurn("What is the capital of France?")},
	})
	frames, err := aimock.ReadSSE(resp.Body)
	require.NoError(t, err)

	var start messageStartFrame
	require.NoError(t, frames[0].Decode(&start))
	assert.Equal(t, 14, start.Message.Usage.InputTokens)
	assert.Zero(t, start.Message.Usage.OutputTokens, "output tokens belong to message_delta, not message_start")
	assert.Nil(t, start.Message.StopReason, "stop_reason is null until the turn ends")
	assert.Empty(t, start.Message.Content)

	var delta messageDeltaFrame
	require.NoError(t, frames[len(frames)-2].Decode(&delta))
	assert.Equal(t, StopEndTurn, delta.Delta.StopReason)
	assert.Equal(t, 8, delta.Usage.OutputTokens, "message_delta carries the cumulative output count")
}

func TestStreamingThinkingAndToolUseDeltas(t *testing.T) {
	srv := startServer(t, "bash-tool.yaml")

	resp := post(t, srv, map[string]any{
		"model":    "claude-sonnet-5",
		"stream":   true,
		"messages": []any{userTurn("please list the files")},
	})
	frames, err := aimock.ReadSSE(resp.Body)
	require.NoError(t, err)

	deltaTypes := map[string]int{}
	var thinking, partialJSON string
	for _, frame := range frames {
		if frame.Event != "content_block_delta" {
			continue
		}
		var got blockDeltaFrame
		require.NoError(t, frame.Decode(&got))

		kind, _ := got.Delta["type"].(string)
		deltaTypes[kind]++
		switch kind {
		case "thinking_delta":
			thinking += got.Delta["thinking"].(string)
		case "input_json_delta":
			partialJSON += got.Delta["partial_json"].(string)
		}
	}

	assert.Equal(t, 1, deltaTypes["signature_delta"], "a thinking block is closed by exactly one signature")
	assert.Greater(t, deltaTypes["thinking_delta"], 1, "prose must span several deltas")
	assert.Greater(t, deltaTypes["input_json_delta"], 1, "tool input must span several deltas")
	assert.Equal(t, "The user wants a directory listing, so I should run ls.", thinking)
	assert.JSONEq(t, `{"command":"ls -1","description":"List files in the working directory"}`, partialJSON,
		"partial_json fragments must only parse once concatenated")
}

func TestScriptedErrorIsReturnedWithItsStatus(t *testing.T) {
	srv := startServer(t, "error-overloaded.yaml")

	resp := post(t, srv, map[string]any{
		"model":    "claude-sonnet-5",
		"messages": []any{userTurn("retry me please")},
	})
	require.Equal(t, 529, resp.StatusCode)

	var got errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "error", got.Type)
	assert.Equal(t, "overloaded_error", got.Error.Type)

	// A client that retries reaches the success rule.
	retry := post(t, srv, map[string]any{
		"model":    "claude-sonnet-5",
		"messages": []any{userTurn("retry me please")},
	})
	require.Equal(t, http.StatusOK, retry.StatusCode)
	assert.Empty(t, srv.Remaining())
}

// A miss must be a 4xx: both CLIs retry 5xx, so a 500 would turn a scenario gap
// into a minute of backoff instead of an immediate, readable failure.
func TestUnmatchedRequestFailsLoudlyAndUnretryably(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp := post(t, srv, map[string]any{
		"model":    "claude-sonnet-5",
		"messages": []any{userTurn("something the scenario never mentions")},
	})
	require.Equal(t, aimock.MissStatus, resp.StatusCode)
	assert.Less(t, resp.StatusCode, 500, "a miss must not look retryable")

	var got errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Contains(t, got.Error.Message, "something the scenario never mentions")
	assert.Contains(t, got.Error.Message, "capital of France", "the diagnostic names the rules still on offer")

	require.Len(t, srv.Requests(), 1)
	assert.NotEmpty(t, srv.Requests()[0].Miss)
}

func TestLenientModeAnswersAMiss(t *testing.T) {
	srv := startServer(t, "text-only.yaml", func(o *Options) { o.Lenient = true })

	resp := post(t, srv, map[string]any{
		"model":    "claude-sonnet-5",
		"messages": []any{userTurn("something the scenario never mentions")},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got messageResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Contains(t, got.Content[0].Text, "unmatched request")
	assert.Len(t, srv.Remaining(), 1, "a fallback must not consume the real rule")
}

// Claude Code sends `system` as a block array, not a string.
func TestSystemBlockArrayIsFlattenedForMatching(t *testing.T) {
	scenario, err := aimock.Parse([]byte(`
anthropic:
  - match: {system_contains: "You are Claude Code"}
    respond: {text: "matched on system"}
`))
	require.NoError(t, err)
	srv, err := Start(Options{Scenario: scenario})
	require.NoError(t, err)
	t.Cleanup(srv.Close)

	resp := post(t, srv, map[string]any{
		"model": "claude-sonnet-5",
		"system": []any{
			map[string]any{"type": "text", "text": "You are Claude Code, Anthropic's CLI."},
			map[string]any{"type": "text", "text": "Extra context."},
		},
		"messages": []any{userTurn("hello")},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	require.Len(t, srv.Requests(), 1)
	assert.Equal(t, "You are Claude Code, Anthropic's CLI.\nExtra context.", srv.Requests()[0].Request.System)
}

func TestCountTokensAndModelsAreServed(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	count, err := http.Post(srv.URL()+"/v1/messages/count_tokens", "application/json",
		bytes.NewReader([]byte(`{"model":"claude-sonnet-5","messages":[{"role":"user","content":"hi"}]}`)))
	require.NoError(t, err)
	defer count.Body.Close()
	require.Equal(t, http.StatusOK, count.StatusCode)

	var tokens map[string]int
	require.NoError(t, json.NewDecoder(count.Body).Decode(&tokens))
	assert.Positive(t, tokens["input_tokens"])

	models, err := http.Get(srv.URL() + "/v1/models")
	require.NoError(t, err)
	defer models.Body.Close()
	assert.Equal(t, http.StatusOK, models.StatusCode)
}

// An unimplemented route must name itself rather than 404 opaquely, so a client
// reaching for something the mock lacks shows up as a named gap.
func TestUnknownRouteNamesItself(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp, err := http.Get(srv.URL() + "/v1/complete")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	var got errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Contains(t, got.Error.Message, "does not implement GET /v1/complete")
}

func TestEnvPointsClientsAtTheMock(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	env := map[string]string{}
	for _, item := range srv.Env() {
		key, value, ok := splitEnv(item)
		require.True(t, ok, "env entry %q must be KEY=VALUE", item)
		env[key] = value
	}

	assert.Equal(t, srv.URL(), env["ANTHROPIC_BASE_URL"])
	assert.Equal(t, aimock.DummyKey, env["ANTHROPIC_API_KEY"])
	assert.Equal(t, "1", env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"])
	assert.Equal(t, "1", env["DISABLE_TELEMETRY"])
}

func splitEnv(item string) (string, string, bool) {
	for i := range item {
		if item[i] == '=' {
			return item[:i], item[i+1:], true
		}
	}
	return "", "", false
}

// collectText reassembles the streamed text so the test asserts on the message a
// consumer would build, not on the chunking.
func collectText(t *testing.T, frames []aimock.Frame) string {
	t.Helper()
	var text string
	for _, frame := range frames {
		if frame.Event != "content_block_delta" {
			continue
		}
		var got blockDeltaFrame
		require.NoError(t, frame.Decode(&got))
		if chunk, ok := got.Delta["text"].(string); ok {
			text += chunk
		}
	}
	return text
}
