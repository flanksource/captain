package session

import (
	"strings"
	"testing"
	"time"
)

// longAssistantBody builds a body of at least minChars that ends in a marker, so
// a test can tell "the body survived" from "the body was cut near the end".
func longAssistantBody(minChars int, marker string) string {
	var b strings.Builder
	for word := 0; b.Len() < minChars; word++ {
		b.WriteString("paragraph ")
	}
	b.WriteString(marker)
	return b.String()
}

func transcriptSession(bodies ...string) *Session {
	ts := time.Date(2026, 7, 27, 12, 21, 12, 0, time.UTC)
	s := &Session{ID: "sess-detail", Source: "codex", CWD: "/repo"}
	for index, body := range bodies {
		at := ts.Add(time.Duration(index) * time.Second)
		s.Messages = append(s.Messages, Message{
			Role:       "assistant",
			Parts:      []Part{{Type: PartText, Text: body}},
			Provenance: &Provenance{Timestamp: &at},
		})
	}
	return s
}

// A terminal row is one line, so the transcript shows a bounded preview. HTML has
// no such limit: cutting the body to a terminal width there is what threw away
// most of every long assistant message -- 115 720 of 156 391 stored bodies are
// longer than the ~100 characters that survived.
func TestTranscript_HTMLCarriesTheFullAssistantBody(t *testing.T) {
	const marker = "TAIL-MARKER-9F2A"
	body := longAssistantBody(2000, marker)

	rendered := transcriptSession(body).Pretty()

	html := rendered.HTML()
	if !strings.Contains(html, marker) {
		t.Fatalf("HTML dropped the end of a %d-character body", len(body))
	}
	if !strings.Contains(html, "<details>") {
		t.Fatalf("HTML rendered the body without a detail section:\n%s", html)
	}

	// The terminal keeps the preview: one row, one line, and no body tail.
	ansi := rendered.ANSI()
	if strings.Contains(ansi, marker) {
		t.Fatalf("ANSI rendered the whole body instead of a one-line preview")
	}
	if !strings.Contains(ansi, "paragraph") {
		t.Fatalf("ANSI dropped the preview entirely:\n%s", ansi)
	}

	// Markdown is a document format too, so it carries the body as a block.
	if !strings.Contains(rendered.Markdown(), marker) {
		t.Fatalf("Markdown dropped the end of the body")
	}
}

// Consecutive identical rows collapse into one tagged with a repeat count, which
// was keyed off the truncated preview: two distinct long messages sharing an
// opening sentence rendered as a single row tagged x2 and one of them vanished.
func TestTranscript_DoesNotCollapseMessagesSharingAPreview(t *testing.T) {
	prefix := longAssistantBody(150, "")
	first := prefix + "FIRST-TAIL-4C1D"
	second := prefix + "SECOND-TAIL-8B7E"

	rendered := transcriptSession(first, second).Pretty()

	html := rendered.HTML()
	for _, marker := range []string{"FIRST-TAIL-4C1D", "SECOND-TAIL-8B7E"} {
		if !strings.Contains(html, marker) {
			t.Fatalf("HTML lost the message ending in %s -- the rows collapsed", marker)
		}
	}
	// Rows that really are identical must still fold -- see
	// TestTranscript_CollapsesConsecutiveDuplicateRows -- so this asserts only
	// that a shared preview is no longer enough to make two rows one.
	if strings.Contains(rendered.String(), "×2") {
		t.Fatalf("two distinct messages collapsed into one repeat row:\n%s", rendered.String())
	}
}
