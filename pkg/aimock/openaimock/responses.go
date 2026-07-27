// ABOUTME: The /v1/responses endpoint — the wire API codex drives, non-streaming and SSE.
// ABOUTME: Emits the documented response.* event sequence, one delta frame per chunk.

package openaimock

import (
	"fmt"
	"io"
	"net/http"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/captain/pkg/aimock"
)

// argumentChunk is how many runes of a function call's JSON arguments go in each
// response.function_call_arguments.delta. Small enough that any realistic tool
// input spans several frames.
const argumentChunk = 24

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.writeError(w, r, aimock.Request{}, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	wire, norm, err := decodeResponses(r, body)
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
		// The 200 and the first frames are already on the wire by the time a
		// stream can fail, so there is no status left to set — the note goes in
		// the journal, where a test asserting on Requests() will see it.
		note := ""
		if err := streamResponses(w, model, respond); err != nil {
			note = fmt.Sprintf("stream aborted: %v", err)
			logger.Errorf("openaimock: %s", note)
		}
		s.record(r, norm, http.StatusOK, note)
		return
	}

	s.record(r, norm, http.StatusOK, "")
	writeJSON(w, http.StatusOK, completedResponse(model, respond))
}

// completedResponse is the whole reply, used both as the non-streaming body and
// as the payload of the terminal response.completed frame.
func completedResponse(model string, respond Respond) map[string]any {
	items := respond.items()
	output := make([]map[string]any, 0, len(items))
	for _, it := range items {
		output = append(output, it.done())
	}
	return map[string]any{
		"id":         responseID(model),
		"object":     "response",
		"status":     "completed",
		"model":      model,
		"output":     output,
		"usage":      respond.Usage.responses(),
		"error":      nil,
		"created_at": 0,
	}
}

// inProgressResponse is the envelope carried by response.created and
// response.in_progress: identity only, with no output and no usage yet.
func inProgressResponse(model string) map[string]any {
	return map[string]any{
		"id":         responseID(model),
		"object":     "response",
		"status":     "in_progress",
		"model":      model,
		"output":     []any{},
		"usage":      nil,
		"error":      nil,
		"created_at": 0,
	}
}

// streamResponses renders respond as the documented Responses API event
// sequence: response.created / .in_progress → per item (output_item.added, the
// item's own delta and done frames, output_item.done) → response.completed.
func streamResponses(w http.ResponseWriter, model string, respond Respond) error {
	sse, err := aimock.NewSSE(w)
	if err != nil {
		return err
	}

	envelope := inProgressResponse(model)
	if err := sse.Event("response.created", map[string]any{"type": "response.created", "response": envelope}); err != nil {
		return err
	}
	if err := sse.Event("response.in_progress", map[string]any{"type": "response.in_progress", "response": envelope}); err != nil {
		return err
	}

	for index, it := range respond.items() {
		if err := streamItem(sse, index, it); err != nil {
			return err
		}
	}

	return sse.Event("response.completed", map[string]any{
		"type":     "response.completed",
		"response": completedResponse(model, respond),
	})
}

func streamItem(sse *aimock.SSE, index int, it item) error {
	if err := sse.Event("response.output_item.added", map[string]any{
		"type": "response.output_item.added", "output_index": index, "item": it.added(),
	}); err != nil {
		return err
	}

	var err error
	switch it.Type {
	case "reasoning":
		err = streamReasoning(sse, index, it)
	case "function_call":
		err = streamArguments(sse, index, it)
	default:
		err = streamText(sse, index, it)
	}
	if err != nil {
		return err
	}

	return sse.Event("response.output_item.done", map[string]any{
		"type": "response.output_item.done", "output_index": index, "item": it.done(),
	})
}

func streamText(sse *aimock.SSE, index int, it item) error {
	if err := sse.Event("response.content_part.added", map[string]any{
		"type": "response.content_part.added", "item_id": it.ID, "output_index": index, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}},
	}); err != nil {
		return err
	}

	for _, chunk := range aimock.ChunkText(it.Text) {
		if err := sse.Event("response.output_text.delta", map[string]any{
			"type": "response.output_text.delta", "item_id": it.ID, "output_index": index,
			"content_index": 0, "delta": chunk,
		}); err != nil {
			return err
		}
	}

	if err := sse.Event("response.output_text.done", map[string]any{
		"type": "response.output_text.done", "item_id": it.ID, "output_index": index,
		"content_index": 0, "text": it.Text,
	}); err != nil {
		return err
	}

	return sse.Event("response.content_part.done", map[string]any{
		"type": "response.content_part.done", "item_id": it.ID, "output_index": index, "content_index": 0,
		"part": map[string]any{"type": "output_text", "text": it.Text, "annotations": []any{}},
	})
}

func streamReasoning(sse *aimock.SSE, index int, it item) error {
	if err := sse.Event("response.reasoning_summary_part.added", map[string]any{
		"type": "response.reasoning_summary_part.added", "item_id": it.ID, "output_index": index,
		"summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""},
	}); err != nil {
		return err
	}

	for _, chunk := range aimock.ChunkText(it.Text) {
		if err := sse.Event("response.reasoning_summary_text.delta", map[string]any{
			"type": "response.reasoning_summary_text.delta", "item_id": it.ID, "output_index": index,
			"summary_index": 0, "delta": chunk,
		}); err != nil {
			return err
		}
	}

	if err := sse.Event("response.reasoning_summary_text.done", map[string]any{
		"type": "response.reasoning_summary_text.done", "item_id": it.ID, "output_index": index,
		"summary_index": 0, "text": it.Text,
	}); err != nil {
		return err
	}

	return sse.Event("response.reasoning_summary_part.done", map[string]any{
		"type": "response.reasoning_summary_part.done", "item_id": it.ID, "output_index": index,
		"summary_index": 0, "part": map[string]any{"type": "summary_text", "text": it.Text},
	})
}

func streamArguments(sse *aimock.SSE, index int, it item) error {
	for _, chunk := range chunkRunes(it.Arguments, argumentChunk) {
		if err := sse.Event("response.function_call_arguments.delta", map[string]any{
			"type": "response.function_call_arguments.delta", "item_id": it.ID,
			"output_index": index, "delta": chunk,
		}); err != nil {
			return err
		}
	}

	return sse.Event("response.function_call_arguments.done", map[string]any{
		"type": "response.function_call_arguments.done", "item_id": it.ID,
		"output_index": index, "arguments": it.Arguments,
	})
}

// chunkRunes splits compact JSON into fixed-size pieces on rune boundaries. A
// function call's arguments only parse once every fragment is concatenated, so
// emitting several proves the consumer accumulates rather than parsing each
// delta on its own.
func chunkRunes(raw string, size int) []string {
	runes := []rune(raw)
	if len(runes) == 0 {
		return nil
	}
	var chunks []string
	for start := 0; start < len(runes); start += size {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[start:end]))
	}
	return chunks
}

func responseID(model string) string { return fmt.Sprintf("resp_mock_%s", model) }

func modelOrDefault(model string) string {
	if model == "" {
		return "gpt-mock"
	}
	return model
}
