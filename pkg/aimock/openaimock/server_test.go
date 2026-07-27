package openaimock

import (
	"bytes"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/flanksource/captain/pkg/aimock"
)

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

func post(t *testing.T, srv *Server, path string, body map[string]any) *http.Response {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	resp, err := http.Post(srv.URL()+path, "application/json", bytes.NewReader(raw))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	var got map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	return got
}

// userInput is the Responses API's item form of a single user turn.
func userInput(text string) []any {
	return []any{map[string]any{
		"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}}
}

func TestResponsesNonStreamingText(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp := post(t, srv, "/v1/responses", map[string]any{
		"model": "gpt-5",
		"input": userInput("What is the capital of France?"),
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got := decodeJSON(t, resp)
	assert.Equal(t, "response", got["object"])
	assert.Equal(t, "completed", got["status"])

	output := got["output"].([]any)
	require.Len(t, output, 1)
	message := output[0].(map[string]any)
	assert.Equal(t, "message", message["type"])
	assert.Equal(t, "assistant", message["role"])

	content := message["content"].([]any)[0].(map[string]any)
	assert.Equal(t, "output_text", content["type"])
	assert.Equal(t, "The capital of France is Paris.", content["text"])

	usage := got["usage"].(map[string]any)
	assert.EqualValues(t, 14, usage["input_tokens"])
	assert.EqualValues(t, 8, usage["output_tokens"])
	assert.EqualValues(t, 22, usage["total_tokens"])
	assert.Empty(t, srv.Remaining())
}

// A bare string input is the single-user-turn shorthand and must match the same
// rules as the item form.
func TestResponsesAcceptsBareStringInput(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp := post(t, srv, "/v1/responses", map[string]any{
		"model": "gpt-5",
		"input": "What is the capital of France?",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Empty(t, srv.Remaining())
}

func TestResponsesFunctionCallOutputConsumesTheNextRule(t *testing.T) {
	srv := startServer(t, "bash-tool.yaml")

	first := post(t, srv, "/v1/responses", map[string]any{
		"model": "gpt-5",
		"input": userInput("please list the files"),
	})
	require.Equal(t, http.StatusOK, first.StatusCode)

	got := decodeJSON(t, first)
	output := got["output"].([]any)
	require.Len(t, output, 2, "reasoning then function_call, in wire order")
	assert.Equal(t, "reasoning", output[0].(map[string]any)["type"])

	call := output[1].(map[string]any)
	assert.Equal(t, "function_call", call["type"])
	assert.Equal(t, "shell", call["name"])
	assert.Equal(t, "call_mock_shell", call["call_id"])
	assert.JSONEq(t, `{"command":["bash","-lc","ls -1"]}`, call["arguments"].(string))

	// The second turn returns the result under the same call_id, which is all the
	// wire carries — the mock resolves it back to the tool name to match on.
	second := post(t, srv, "/v1/responses", map[string]any{
		"model": "gpt-5",
		"input": []any{
			map[string]any{"type": "message", "role": "user",
				"content": []any{map[string]any{"type": "input_text", "text": "please list the files"}}},
			map[string]any{"type": "function_call", "name": "shell", "call_id": "call_mock_shell", "arguments": "{}"},
			map[string]any{"type": "function_call_output", "call_id": "call_mock_shell", "output": "README.md\nmain.go"},
		},
	})
	require.Equal(t, http.StatusOK, second.StatusCode)

	final := decodeJSON(t, second)["output"].([]any)[0].(map[string]any)
	content := final["content"].([]any)[0].(map[string]any)
	assert.Equal(t, "The directory contains README.md and main.go.", content["text"])
	assert.Empty(t, srv.Remaining())
}

func TestResponsesStreamingEventSequence(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp := post(t, srv, "/v1/responses", map[string]any{
		"model": "gpt-5", "stream": true,
		"input": userInput("What is the capital of France?"),
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	frames, err := aimock.ReadSSE(resp.Body)
	require.NoError(t, err)

	names := aimock.EventNames(frames)
	assert.Equal(t, []string{
		"response.created", "response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta", "response.output_text.delta", "response.output_text.delta",
		"response.output_text.delta", "response.output_text.delta", "response.output_text.delta",
		"response.output_text.done", "response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}, names)

	for i, frame := range frames {
		assert.Equal(t, frame.Event, frame.Type(), "frame %d payload type must match its event name", i)
	}

	var text string
	for _, frame := range frames {
		if frame.Event != "response.output_text.delta" {
			continue
		}
		var delta struct {
			Delta string `json:"delta"`
		}
		require.NoError(t, frame.Decode(&delta))
		text += delta.Delta
	}
	assert.Equal(t, "The capital of France is Paris.", text)
}

func TestResponsesStreamingFunctionCallArguments(t *testing.T) {
	srv := startServer(t, "bash-tool.yaml")

	resp := post(t, srv, "/v1/responses", map[string]any{
		"model": "gpt-5", "stream": true,
		"input": userInput("please list the files"),
	})
	frames, err := aimock.ReadSSE(resp.Body)
	require.NoError(t, err)

	var arguments string
	var deltas int
	for _, frame := range frames {
		if frame.Event != "response.function_call_arguments.delta" {
			continue
		}
		var got struct {
			Delta string `json:"delta"`
		}
		require.NoError(t, frame.Decode(&got))
		arguments += got.Delta
		deltas++
	}

	assert.Greater(t, deltas, 1, "arguments must span several deltas")
	assert.JSONEq(t, `{"command":["bash","-lc","ls -1"]}`, arguments,
		"argument fragments must only parse once concatenated")

	// The reasoning item streams its own summary frames ahead of the call.
	assert.Contains(t, aimock.EventNames(frames), "response.reasoning_summary_text.delta")

	// response.completed repeats the finished items, which is what a consumer
	// that ignored the deltas would read.
	last := frames[len(frames)-1]
	require.Equal(t, "response.completed", last.Event)
	var completed struct {
		Response struct {
			Output []map[string]any `json:"output"`
		} `json:"response"`
	}
	require.NoError(t, last.Decode(&completed))
	require.Len(t, completed.Response.Output, 2)
	assert.Equal(t, "completed", completed.Response.Output[1]["status"])
}

func TestChatCompletionsNonStreaming(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp := post(t, srv, "/v1/chat/completions", map[string]any{
		"model": "gpt-5",
		"messages": []any{
			map[string]any{"role": "system", "content": "You are terse."},
			map[string]any{"role": "user", "content": "What is the capital of France?"},
		},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got := decodeJSON(t, resp)
	assert.Equal(t, "chat.completion", got["object"])

	choice := got["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, FinishStop, choice["finish_reason"])
	message := choice["message"].(map[string]any)
	assert.Equal(t, "assistant", message["role"])
	assert.Equal(t, "The capital of France is Paris.", message["content"])

	usage := got["usage"].(map[string]any)
	assert.EqualValues(t, 14, usage["prompt_tokens"])
	assert.EqualValues(t, 8, usage["completion_tokens"])

	require.Len(t, srv.Requests(), 1)
	assert.Equal(t, "You are terse.", srv.Requests()[0].Request.System)
}

func TestChatCompletionsToolCallAndToolReply(t *testing.T) {
	srv := startServer(t, "bash-tool.yaml")

	first := post(t, srv, "/v1/chat/completions", map[string]any{
		"model":    "gpt-5",
		"messages": []any{map[string]any{"role": "user", "content": "please list the files"}},
	})
	require.Equal(t, http.StatusOK, first.StatusCode)

	choice := decodeJSON(t, first)["choices"].([]any)[0].(map[string]any)
	assert.Equal(t, FinishToolCalls, choice["finish_reason"])

	message := choice["message"].(map[string]any)
	assert.Nil(t, message["content"], "a tool-calling choice carries null content, not empty string")
	assert.NotEmpty(t, message["reasoning_content"])

	call := message["tool_calls"].([]any)[0].(map[string]any)
	assert.Equal(t, "call_mock_shell", call["id"])
	assert.Equal(t, "shell", call["function"].(map[string]any)["name"])

	// A `tool` message is the user side of the loop, so it advances the scenario.
	second := post(t, srv, "/v1/chat/completions", map[string]any{
		"model": "gpt-5",
		"messages": []any{
			map[string]any{"role": "user", "content": "please list the files"},
			map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{
				map[string]any{"id": "call_mock_shell", "type": "function",
					"function": map[string]any{"name": "shell", "arguments": "{}"}},
			}},
			map[string]any{"role": "tool", "tool_call_id": "call_mock_shell", "content": "README.md\nmain.go"},
		},
	})
	require.Equal(t, http.StatusOK, second.StatusCode)

	final := decodeJSON(t, second)["choices"].([]any)[0].(map[string]any)["message"].(map[string]any)
	assert.Equal(t, "The directory contains README.md and main.go.", final["content"])
	assert.Empty(t, srv.Remaining())
}

func TestChatCompletionsStreamingEndsWithDone(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp := post(t, srv, "/v1/chat/completions", map[string]any{
		"model": "gpt-5", "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": "What is the capital of France?"}},
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	frames, err := aimock.ReadSSE(resp.Body)
	require.NoError(t, err)
	require.NotEmpty(t, frames)

	for _, frame := range frames {
		assert.Empty(t, frame.Event, "chat completions frames are unnamed `data:` lines")
	}
	assert.Equal(t, aimock.DoneSentinel, frames[len(frames)-1].Data)

	var text, finish string
	var usage map[string]any
	for _, frame := range frames[:len(frames)-1] {
		var chunk struct {
			Object  string `json:"object"`
			Choices []struct {
				Delta        map[string]any `json:"delta"`
				FinishReason *string        `json:"finish_reason"`
			} `json:"choices"`
			Usage map[string]any `json:"usage"`
		}
		require.NoError(t, frame.Decode(&chunk))
		assert.Equal(t, "chat.completion.chunk", chunk.Object)

		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, choice := range chunk.Choices {
			if part, ok := choice.Delta["content"].(string); ok {
				text += part
			}
			if choice.FinishReason != nil {
				finish = *choice.FinishReason
			}
		}
	}

	assert.Equal(t, "The capital of France is Paris.", text)
	assert.Equal(t, FinishStop, finish)
	require.NotNil(t, usage, "the stream must report usage in its own choice-less chunk")
	assert.EqualValues(t, 22, usage["total_tokens"])
}

func TestChatCompletionsStreamingToolCall(t *testing.T) {
	srv := startServer(t, "bash-tool.yaml")

	resp := post(t, srv, "/v1/chat/completions", map[string]any{
		"model": "gpt-5", "stream": true,
		"messages": []any{map[string]any{"role": "user", "content": "please list the files"}},
	})
	frames, err := aimock.ReadSSE(resp.Body)
	require.NoError(t, err)

	var name, arguments string
	for _, frame := range frames {
		if frame.Data == aimock.DoneSentinel {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					ToolCalls []struct {
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		require.NoError(t, frame.Decode(&chunk))
		for _, choice := range chunk.Choices {
			for _, call := range choice.Delta.ToolCalls {
				if call.Function.Name != "" {
					name = call.Function.Name
				}
				arguments += call.Function.Arguments
			}
		}
	}

	assert.Equal(t, "shell", name)
	assert.JSONEq(t, `{"command":["bash","-lc","ls -1"]}`, arguments)
}

func TestScriptedErrorIsReturnedWithItsStatus(t *testing.T) {
	srv := startServer(t, "error-overloaded.yaml")

	resp := post(t, srv, "/v1/responses", map[string]any{
		"model": "gpt-5", "input": "retry me please",
	})
	require.Equal(t, http.StatusTooManyRequests, resp.StatusCode)

	var got errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "rate_limit_error", got.Error.Type)
	require.NotNil(t, got.Error.Code)
	assert.Equal(t, "rate_limit_exceeded", *got.Error.Code)

	retry := post(t, srv, "/v1/responses", map[string]any{
		"model": "gpt-5", "input": "retry me please",
	})
	require.Equal(t, http.StatusOK, retry.StatusCode)
	assert.Empty(t, srv.Remaining())
}

func TestUnmatchedRequestFailsLoudlyAndUnretryably(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp := post(t, srv, "/v1/responses", map[string]any{
		"model": "gpt-5", "input": "something the scenario never mentions",
	})
	require.Equal(t, aimock.MissStatus, resp.StatusCode)
	assert.Less(t, resp.StatusCode, 500, "a miss must not look retryable")

	var got errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Contains(t, got.Error.Message, "something the scenario never mentions")
	assert.Contains(t, got.Error.Message, "capital of France")
}

func TestLenientModeAnswersAMiss(t *testing.T) {
	srv := startServer(t, "text-only.yaml", func(o *Options) { o.Lenient = true })

	resp := post(t, srv, "/v1/responses", map[string]any{
		"model": "gpt-5", "input": "something the scenario never mentions",
	})
	require.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Len(t, srv.Remaining(), 1, "a fallback must not consume the real rule")
}

// codex probes /v1/models on startup; missing it fails the run before any
// scenario rule is reached.
func TestModelsIsServed(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp, err := http.Get(srv.URL() + "/v1/models")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var got struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Equal(t, "list", got.Object)
	assert.NotEmpty(t, got.Data)
}

func TestUnknownRouteNamesItself(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	resp, err := http.Get(srv.URL() + "/v1/embeddings")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusNotFound, resp.StatusCode)

	var got errorResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&got))
	assert.Contains(t, got.Error.Message, "does not implement GET /v1/embeddings")
}

func TestEnvAndAPIURLPointAtTheMock(t *testing.T) {
	srv := startServer(t, "text-only.yaml")

	assert.Equal(t, srv.URL()+"/v1", srv.APIURL())
	assert.Equal(t, []string{
		"OPENAI_BASE_URL=" + srv.APIURL(),
		"OPENAI_API_KEY=" + aimock.DummyKey,
	}, srv.Env())
}
