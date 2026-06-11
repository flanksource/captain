package aichat

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// sseWriter emits the Vercel AI SDK v6 "UI Message Stream" protocol: SSE where
// each part is `data: {compact-json}\n\n`, terminated by `data: [DONE]\n\n`.
// Every part is a strict object — optional fields are omitted, never null.
//
// Part reference: https://ai-sdk.dev/docs/ai-sdk-ui/stream-protocol
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// newSSEWriter writes the required v6 headers and returns a writer. It errors if
// the ResponseWriter cannot flush (streaming is impossible otherwise).
func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support flushing")
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("x-vercel-ai-ui-message-stream", "v1")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("x-accel-buffering", "no")
	w.WriteHeader(http.StatusOK)
	return &sseWriter{w: w, flusher: flusher}, nil
}

func (s *sseWriter) part(p map[string]any) error {
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *sseWriter) done() error {
	if _, err := fmt.Fprint(s.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// --- stream lifecycle parts ---

func (s *sseWriter) start() error      { return s.part(map[string]any{"type": "start"}) }
func (s *sseWriter) startStep() error  { return s.part(map[string]any{"type": "start-step"}) }
func (s *sseWriter) finishStep() error { return s.part(map[string]any{"type": "finish-step"}) }
func (s *sseWriter) finish() error     { return s.part(map[string]any{"type": "finish"}) }

// --- text parts ---

func (s *sseWriter) textStart(id string) error {
	return s.part(map[string]any{"type": "text-start", "id": id})
}

// textDelta uses wire field `delta` (NOT `text`) — a v6 protocol requirement.
func (s *sseWriter) textDelta(id, delta string) error {
	return s.part(map[string]any{"type": "text-delta", "id": id, "delta": delta})
}

func (s *sseWriter) textEnd(id string) error {
	return s.part(map[string]any{"type": "text-end", "id": id})
}

// --- tool parts ---

func (s *sseWriter) toolInputAvailable(callID, name string, input any) error {
	return s.part(map[string]any{
		"type":       "tool-input-available",
		"toolCallId": callID,
		"toolName":   name,
		"input":      input,
	})
}

func (s *sseWriter) toolOutputAvailable(callID string, output any) error {
	return s.part(map[string]any{
		"type":       "tool-output-available",
		"toolCallId": callID,
		"output":     output,
	})
}

// --- error ---

func (s *sseWriter) errorPart(message string) error {
	return s.part(map[string]any{"type": "error", "errorText": message})
}
