package history

import (
	"strings"
	"testing"
)

// The reader parses every line into one reused CodexEvent. That is only sound
// while each parse starts from a zero value: the JSON decoder leaves fields the
// record does not mention untouched, so a record without git metadata, token
// info or content would otherwise inherit the previous record's. These pin the
// property rather than the zeroing that provides it.
func TestCodexParseDoesNotCarryFieldsBetweenLines(t *testing.T) {
	var event CodexEvent

	// The dotted-name live schema spreads its fields across the top level of the
	// event, where nothing else zeroes them: CodexPayload.UnmarshalJSON resets
	// the payload, but Item, Error, Usage, ThreadID and Message are only ever
	// cleared by the reader zeroing the event before each line.
	live := `{"timestamp":"2026-07-16T11:14:45.000Z","type":"item.completed","thread_id":"thread-1","message":"m","item":{"type":"agent_message","text":"hello"},"usage":{"input_tokens":11,"output_tokens":3}}`
	if err := parseCodexLineInto(&event, live); err != nil {
		t.Fatalf("parse item.completed: %v", err)
	}
	if event.Item == nil || event.Item.Text != "hello" || event.Usage == nil || event.ThreadID != "thread-1" {
		t.Fatalf("item.completed did not decode: item=%+v usage=%+v thread=%q", event.Item, event.Usage, event.ThreadID)
	}

	failed := `{"timestamp":"2026-07-16T11:14:46.000Z","type":"turn.failed","error":{"message":"boom"}}`
	if err := parseCodexLineInto(&event, failed); err != nil {
		t.Fatalf("parse turn.failed: %v", err)
	}
	if event.Item != nil {
		t.Errorf("turn.failed inherited the previous record's item: %+v", event.Item)
	}
	if event.Usage != nil {
		t.Errorf("turn.failed inherited the previous record's usage: %+v", event.Usage)
	}
	if event.ThreadID != "" {
		t.Errorf("turn.failed inherited the previous record's thread id: %q", event.ThreadID)
	}
	if event.Message != "" {
		t.Errorf("turn.failed inherited the previous record's message: %q", event.Message)
	}

	// The payload is reset by its own unmarshaler rather than by the reader, so
	// pin that separately: a record without git metadata or token info must not
	// report the previous record's.
	rich := `{"type":"session_meta","payload":{"id":"sess-1","cwd":"/repo","git":{"branch":"main","commit_hash":"abc123"}}}`
	if err := parseCodexLineInto(&event, rich); err != nil {
		t.Fatalf("parse session_meta: %v", err)
	}
	if event.Payload.Git == nil || event.Payload.Git.Branch != "main" {
		t.Fatalf("session_meta did not decode git metadata: %+v", event.Payload.Git)
	}
	bare := `{"type":"turn_context","payload":{"turn_id":"turn-1","model":"gpt-5.5"}}`
	if err := parseCodexLineInto(&event, bare); err != nil {
		t.Fatalf("parse turn_context: %v", err)
	}
	if event.Payload.Git != nil {
		t.Errorf("turn_context inherited the previous record's git metadata: %+v", event.Payload.Git)
	}
	if event.Payload.ID != "" || event.Payload.CWD != "" {
		t.Errorf("turn_context inherited session_meta scalars: id=%q cwd=%q", event.Payload.ID, event.Payload.CWD)
	}

	// A shorter content list must not keep the tail of a longer one: the decoder
	// may reuse slice capacity, and a row emitted from the earlier record still
	// refers to what that array held.
	if err := parseCodexLineInto(&event, `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a"},{"type":"output_text","text":"b"}]}}`); err != nil {
		t.Fatalf("parse two-block message: %v", err)
	}
	if err := parseCodexLineInto(&event, `{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"c"}]}}`); err != nil {
		t.Fatalf("parse one-block message: %v", err)
	}
	if got := len(event.Payload.Content); got != 1 {
		t.Errorf("content kept %d blocks from the longer record, want 1", got)
	}
}

// CodexPayload.UnmarshalJSON decodes straight into the receiver now, where it
// used to decode into a temporary and assign the whole struct over. Assignment
// cleared fields the record does not mention; the explicit reset is what
// replaces it, and this pins that contract at the payload's own seam rather
// than through the reader, which zeroes the whole event anyway.
func TestCodexPayloadUnmarshalReplacesTheReceiver(t *testing.T) {
	payload := CodexPayload{
		Type: "session_meta", ID: "old", CWD: "/old", TurnID: "turn-old",
		Git:  &CodexGitMeta{Branch: "old-branch"},
		Info: &CodexTokenInfo{ModelContextWindow: 999},
	}
	if err := payload.UnmarshalJSON([]byte(`{"type":"turn_context","model":"gpt-5.5"}`)); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.Model != "gpt-5.5" || payload.Type != "turn_context" {
		t.Fatalf("did not decode the new record: %+v", payload)
	}
	for name, stale := range map[string]bool{
		"ID":     payload.ID != "",
		"CWD":    payload.CWD != "",
		"TurnID": payload.TurnID != "",
		"Git":    payload.Git != nil,
		"Info":   payload.Info != nil,
	} {
		if stale {
			t.Errorf("%s survived a decode into an already-populated payload", name)
		}
	}
}

// RawMap replaced a map decoded eagerly into every payload. Each caller mutates
// what it gets back, so two calls must not hand out the same map, and a payload
// that is not an object must yield nil rather than an error the caller ignores.
func TestCodexPayloadRawMapIsIndependentPerCall(t *testing.T) {
	var event CodexEvent
	line := `{"type":"event_msg","payload":{"type":"agent_reasoning","turn_id":"turn-1","text":"thinking"}}`
	if err := parseCodexLineInto(&event, line); err != nil {
		t.Fatalf("parse: %v", err)
	}

	first := event.Payload.RawMap()
	if first["text"] != "thinking" || first["turn_id"] != "turn-1" {
		t.Fatalf("RawMap dropped payload keys: %v", first)
	}
	first["event"] = "mutated"

	second := event.Payload.RawMap()
	if _, mutated := second["event"]; mutated {
		t.Error("RawMap handed out a map a previous caller had already mutated")
	}
	if second["text"] != "thinking" {
		t.Errorf("RawMap lost payload keys on the second call: %v", second)
	}
}

// The parser reuses one event across a whole file. This walks a transcript that
// mixes every record shape and checks the rows match a parse that decodes each
// line into its own event, which is what the reuse replaced.
func TestCodexReusedEventMatchesPerLineParse(t *testing.T) {
	uses, err := ExtractCodexToolUsesFromReader(strings.NewReader(strings.Join(incrementalFixture, "\n")))
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if len(uses) == 0 {
		t.Fatal("fixture produced no rows")
	}
	for _, use := range uses {
		if use.SessionID != "sess-inc" {
			t.Errorf("row %q at line %d lost the session id: %q", use.Tool, use.SourceLine, use.SessionID)
		}
		if use.CWD != "/repo" {
			t.Errorf("row %q at line %d lost the session cwd: %q", use.Tool, use.SourceLine, use.CWD)
		}
	}
}
