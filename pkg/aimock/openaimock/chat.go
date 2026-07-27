// ABOUTME: The /v1/chat/completions endpoint — what OpenAI-compatible gateways and the deepseek path use.
// ABOUTME: Same scripted replies as /v1/responses, rendered into the older choices/delta shape.

package openaimock

import (
	"fmt"
	"io"
	"net/http"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/captain/pkg/aimock"
)

func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, r, aimock.Request{}, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	wire, norm, err := decodeChat(r, body)
	if err != nil {
		s.writeError(w, r, norm, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	respond, ok := s.resolve(w, r, norm)
	if !ok {
		return
	}

	model := modelOrDefault(wire.Model)
	if wire.Stream {
		note := ""
		if err := streamChat(w, model, respond); err != nil {
			note = fmt.Sprintf("stream aborted: %v", err)
			logger.Errorf("openaimock: %s", note)
		}
		s.record(r, norm, http.StatusOK, note)
		return
	}

	s.record(r, norm, http.StatusOK, "")
	writeJSON(w, http.StatusOK, chatCompletion(model, respond))
}

// chatCompletion renders the reply as a complete non-streaming completion.
// Reasoning rides on the non-standard `reasoning_content` field, which is what
// the deepseek-compatible endpoints captain talks to actually emit.
func chatCompletion(model string, respond Respond) map[string]any {
	message := map[string]any{"role": "assistant", "content": respond.Text}
	if respond.Reasoning != "" {
		message["reasoning_content"] = respond.Reasoning
	}
	if call := chatToolCallPayload(respond); call != nil {
		message["tool_calls"] = []any{call}
		// A tool-calling choice carries no prose; content is explicitly null
		// rather than "" so a consumer distinguishing the two sees the right one.
		message["content"] = nil
	}

	return map[string]any{
		"id":      completionID(model),
		"object":  "chat.completion",
		"created": 0,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": respond.resolvedFinishReason(),
		}},
		"usage": respond.Usage.chat(),
	}
}

// chatToolCallPayload renders the scripted function call in the tool_calls shape,
// or nil when the reply makes no call.
func chatToolCallPayload(respond Respond) map[string]any {
	for _, it := range respond.items() {
		if it.Type != "function_call" {
			continue
		}
		return map[string]any{
			"index":    0,
			"id":       it.CallID,
			"type":     "function",
			"function": map[string]any{"name": it.Name, "arguments": it.Arguments},
		}
	}
	return nil
}

// streamChat renders respond as chat.completion.chunk frames: an opening role
// delta, one delta per content chunk, a terminal finish_reason delta, a
// usage-only chunk, then the [DONE] sentinel.
func streamChat(w http.ResponseWriter, model string, respond Respond) error {
	sse, err := aimock.NewSSE(w)
	if err != nil {
		return err
	}

	chunk := func(delta map[string]any, finish any) error {
		return sse.Data(map[string]any{
			"id": completionID(model), "object": "chat.completion.chunk", "created": 0, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		})
	}

	if err := chunk(map[string]any{"role": "assistant", "content": ""}, nil); err != nil {
		return err
	}

	for _, part := range aimock.ChunkText(respond.Reasoning) {
		if err := chunk(map[string]any{"reasoning_content": part}, nil); err != nil {
			return err
		}
	}

	if call := chatToolCallPayload(respond); call != nil {
		if err := streamChatToolCall(chunk, call); err != nil {
			return err
		}
	}

	for _, part := range aimock.ChunkText(respond.Text) {
		if err := chunk(map[string]any{"content": part}, nil); err != nil {
			return err
		}
	}

	if err := chunk(map[string]any{}, respond.resolvedFinishReason()); err != nil {
		return err
	}

	// Usage arrives in its own choice-less chunk, matching what the API sends
	// under stream_options.include_usage.
	if err := sse.Data(map[string]any{
		"id": completionID(model), "object": "chat.completion.chunk", "created": 0, "model": model,
		"choices": []any{}, "usage": respond.Usage.chat(),
	}); err != nil {
		return err
	}

	return sse.Done()
}

// streamChatToolCall splits the call across frames the way the API does: the
// first names the function, the rest carry arguments only.
func streamChatToolCall(chunk func(map[string]any, any) error, call map[string]any) error {
	function, _ := call["function"].(map[string]any)
	arguments, _ := function["arguments"].(string)

	opening := map[string]any{
		"index": 0, "id": call["id"], "type": "function",
		"function": map[string]any{"name": function["name"], "arguments": ""},
	}
	if err := chunk(map[string]any{"tool_calls": []any{opening}}, nil); err != nil {
		return err
	}

	for _, part := range chunkRunes(arguments, argumentChunk) {
		frame := map[string]any{"index": 0, "function": map[string]any{"arguments": part}}
		if err := chunk(map[string]any{"tool_calls": []any{frame}}, nil); err != nil {
			return err
		}
	}
	return nil
}

func completionID(model string) string { return fmt.Sprintf("chatcmpl_mock_%s", model) }
