package aichat

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// flushRecorder is an httptest.ResponseRecorder that also satisfies
// http.Flusher, which newSSEWriter requires.
type flushRecorder struct {
	*httptest.ResponseRecorder
}

func (f *flushRecorder) Flush() {}

func newRecorder() *flushRecorder {
	return &flushRecorder{httptest.NewRecorder()}
}

func TestSSEWriterHeaders(t *testing.T) {
	rec := newRecorder()
	if _, err := newSSEWriter(rec); err != nil {
		t.Fatalf("newSSEWriter: %v", err)
	}
	want := map[string]string{
		"Content-Type":                   "text/event-stream",
		"x-vercel-ai-ui-message-stream":  "v1",
		"Cache-Control":                  "no-cache",
	}
	for k, v := range want {
		if got := rec.Header().Get(k); got != v {
			t.Errorf("header %s = %q, want %q", k, got, v)
		}
	}
}

func TestSSEPlainTextTurn(t *testing.T) {
	rec := newRecorder()
	s, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("newSSEWriter: %v", err)
	}
	for _, step := range []func() error{
		s.start,
		s.startStep,
		func() error { return s.textStart("text-0") },
		func() error { return s.textDelta("text-0", "Hello") },
		func() error { return s.textDelta("text-0", " world") },
		func() error { return s.textEnd("text-0") },
		s.finishStep,
		s.finish,
		s.done,
	} {
		if err := step(); err != nil {
			t.Fatalf("emit: %v", err)
		}
	}

	want := strings.Join([]string{
		`data: {"type":"start"}`,
		`data: {"type":"start-step"}`,
		`data: {"id":"text-0","type":"text-start"}`,
		`data: {"delta":"Hello","id":"text-0","type":"text-delta"}`,
		`data: {"delta":" world","id":"text-0","type":"text-delta"}`,
		`data: {"id":"text-0","type":"text-end"}`,
		`data: {"type":"finish-step"}`,
		`data: {"type":"finish"}`,
		`data: [DONE]`,
		``,
		``,
	}, "\n\n")
	// Each part is "data: ...\n\n"; the join above approximates the framing;
	// compare on the meaningful data: lines instead of exact whitespace.
	assertDataLines(t, rec.Body.String(), want)
}

func TestSSEToolRoundTrip(t *testing.T) {
	rec := newRecorder()
	s, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("newSSEWriter: %v", err)
	}
	_ = s.start()
	_ = s.startStep()
	_ = s.toolInputAvailable("call_1", "listThings", map[string]any{"limit": 5})
	_ = s.toolOutputAvailable("call_1", map[string]any{"count": 2})
	_ = s.textStart("text-0")
	_ = s.textDelta("text-0", "Found 2")
	_ = s.textEnd("text-0")
	_ = s.finishStep()
	_ = s.finish()
	_ = s.done()

	body := rec.Body.String()
	for _, frag := range []string{
		`"type":"tool-input-available"`,
		`"toolCallId":"call_1"`,
		`"toolName":"listThings"`,
		`"type":"tool-output-available"`,
		`"type":"text-delta"`,
		`"delta":"Found 2"`,
		"data: [DONE]",
	} {
		if !strings.Contains(body, frag) {
			t.Errorf("body missing %q\n--- body ---\n%s", frag, body)
		}
	}
	// text-delta must carry `delta`, never `text` (v6 requirement).
	if strings.Contains(body, `"type":"text-delta","text":`) || strings.Contains(body, `"text":"Found 2"`) {
		t.Errorf("text-delta used field `text` instead of `delta`:\n%s", body)
	}
}

// assertDataLines compares only the `data: ` lines (ignoring blank-line framing)
// between got and a want blob.
func assertDataLines(t *testing.T, got, want string) {
	t.Helper()
	gl := dataLines(got)
	wl := dataLines(want)
	if len(gl) != len(wl) {
		t.Fatalf("got %d data lines, want %d\ngot:\n%s", len(gl), len(wl), got)
	}
	for i := range gl {
		if gl[i] != wl[i] {
			t.Errorf("data line %d:\n got %s\nwant %s", i, gl[i], wl[i])
		}
	}
}

func dataLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "data: ") {
			out = append(out, line)
		}
	}
	return out
}
