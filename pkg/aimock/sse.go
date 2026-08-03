// ABOUTME: Server-sent-event frame writer shared by both mock servers.
// ABOUTME: Flushes every frame so streaming consumers see deltas arrive incrementally, not in one burst.

package aimock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"syscall"
)

func WaitForCancellation(ctx context.Context, hold bool) error {
	if !hold {
		return nil
	}
	<-ctx.Done()
	return ctx.Err()
}

func IsClientCancellation(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		ctx.Err() != nil
}

// SSE writes server-sent-event frames to an http.ResponseWriter.
type SSE struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// NewSSE sets the streaming response headers and returns a frame writer. It
// fails when the ResponseWriter cannot flush, because an unflushed mock stream
// would deliver every event at once and hide ordering bugs in the consumer.
func NewSSE(w http.ResponseWriter) (*SSE, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("aimock: ResponseWriter does not support flushing; cannot stream")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	return &SSE{w: w, flusher: flusher}, nil
}

// Event writes one named frame carrying payload as JSON.
func (s *SSE) Event(name string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal %s event: %w", name, err)
	}
	if _, err := fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", name, data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// Data writes an unnamed frame — the OpenAI chat-completions style, which sends
// bare `data:` lines with no event name.
func (s *SSE) Data(payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal data event: %w", err)
	}
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", data); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// Done writes the `data: [DONE]` sentinel that terminates an OpenAI stream.
func (s *SSE) Done() error {
	if _, err := fmt.Fprint(s.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// ChunkText splits text into deterministic streaming chunks — one per
// whitespace-delimited word, keeping the trailing space so the concatenation of
// all chunks is the original string. Deterministic chunking means a test can
// assert on the exact delta sequence, and multi-chunk output exercises consumer
// reassembly that a single-chunk reply would not.
func ChunkText(text string) []string {
	if text == "" {
		return nil
	}
	fields := strings.SplitAfter(text, " ")
	chunks := make([]string, 0, len(fields))
	for _, field := range fields {
		if field != "" {
			chunks = append(chunks, field)
		}
	}
	if len(chunks) == 0 {
		return []string{text}
	}
	return chunks
}
