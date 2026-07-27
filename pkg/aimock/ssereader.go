// ABOUTME: Reader for the frames the mock servers emit, so a caller can assert on an exact event sequence.
// ABOUTME: Lives beside the writer because "what did the stream actually look like" is the assertion surface.

package aimock

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// DoneSentinel is the payload OpenAI streams end with, in place of JSON.
const DoneSentinel = "[DONE]"

// Frame is one server-sent event. Event is empty for the unnamed `data:` frames
// the OpenAI protocol uses.
type Frame struct {
	Event string
	Data  string
}

// Type reads the frame's own `type` field, which is how both protocols name the
// event inside the payload. Empty when the payload is not a JSON object.
func (f Frame) Type() string {
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal([]byte(f.Data), &envelope) != nil {
		return ""
	}
	return envelope.Type
}

// Decode unmarshals the frame payload.
func (f Frame) Decode(into any) error {
	if err := json.Unmarshal([]byte(f.Data), into); err != nil {
		return fmt.Errorf("decode %s frame: %w", f.Event, err)
	}
	return nil
}

// ReadSSE reads a whole stream into frames. It is for tests and diagnostics —
// it buffers everything rather than yielding incrementally.
func ReadSSE(r io.Reader) ([]Frame, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var frames []Frame
	var current Frame
	var data []string

	flush := func() {
		if current.Event == "" && len(data) == 0 {
			return
		}
		current.Data = strings.Join(data, "\n")
		frames = append(frames, current)
		current, data = Frame{}, nil
	}

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "event:"):
			current.Event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()

	if err := scanner.Err(); err != nil {
		return frames, fmt.Errorf("read sse stream: %w", err)
	}
	return frames, nil
}

// EventNames lists each frame's name, falling back to the payload's `type` for
// the unnamed OpenAI frames. This is the sequence a test asserts on.
func EventNames(frames []Frame) []string {
	out := make([]string, 0, len(frames))
	for _, frame := range frames {
		switch {
		case frame.Event != "":
			out = append(out, frame.Event)
		case frame.Data == DoneSentinel:
			out = append(out, DoneSentinel)
		default:
			out = append(out, frame.Type())
		}
	}
	return out
}
