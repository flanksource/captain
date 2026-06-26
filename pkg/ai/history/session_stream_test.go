package history

import "testing"

func TestParseSessionEventsAssistantBlocks(t *testing.T) {
	line := []byte(`{"type":"assistant","sessionId":"sess-1","cwd":"/repo","message":{"role":"assistant","stop_reason":"tool_use","content":[` +
		`{"type":"thinking","thinking":"let me look"},` +
		`{"type":"text","text":"I'll read the file"},` +
		`{"type":"tool_use","name":"Read","id":"tu1","input":{"file_path":"main.go"}}` +
		`]}}`)

	events, err := ParseSessionEvents(line)
	if err != nil {
		t.Fatalf("ParseSessionEvents: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(events), events)
	}
	if events[0].Kind != EventThinking || events[0].Text != "let me look" {
		t.Errorf("events[0] = %+v, want thinking", events[0])
	}
	if events[1].Kind != EventAssistantText || events[1].Text != "I'll read the file" {
		t.Errorf("events[1] = %+v, want assistant text", events[1])
	}
	if events[2].Kind != EventToolUse || events[2].ToolUse.Tool != "Read" || events[2].ToolUse.Input["file_path"] != "main.go" {
		t.Errorf("events[2] = %+v, want Read tool_use", events[2])
	}
	if events[2].ToolUse.SessionID != "sess-1" || events[2].ToolUse.Source != "claude" {
		t.Errorf("tool_use metadata = %+v, want session/source populated", events[2].ToolUse)
	}
}

func TestParseSessionEventsEndTurn(t *testing.T) {
	line := []byte(`{"type":"assistant","sessionId":"s","message":{"stop_reason":"end_turn","content":[{"type":"text","text":"done"}]}}`)
	events, err := ParseSessionEvents(line)
	if err != nil {
		t.Fatalf("ParseSessionEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 (text + turn_end): %+v", len(events), events)
	}
	last := events[len(events)-1]
	if last.Kind != EventTurnEnd || last.StopReason != "end_turn" {
		t.Errorf("last event = %+v, want turn_end", last)
	}
}

func TestParseSessionEventsAPIError(t *testing.T) {
	// An HTTP API error (rate limit) ends the turn with stop_sequence but must be
	// reported as an error event, not a normal turn end.
	line := []byte(`{"type":"assistant","sessionId":"s","message":{"model":"<synthetic>","stop_reason":"stop_sequence","content":[{"type":"text","text":"API Error: Server is temporarily limiting requests · Rate limited"}]},"error":"rate_limit","isApiErrorMessage":true,"apiErrorStatus":429}`)
	events, err := ParseSessionEvents(line)
	if err != nil {
		t.Fatalf("ParseSessionEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (error only): %+v", len(events), events)
	}
	e := events[0]
	if e.Kind != EventError {
		t.Fatalf("event kind = %q, want %q", e.Kind, EventError)
	}
	if e.ErrorType != "rate_limit" || e.ErrorStatus != 429 {
		t.Errorf("error detail = (%q, %d), want (rate_limit, 429)", e.ErrorType, e.ErrorStatus)
	}
	if e.StopReason != "stop_sequence" {
		t.Errorf("stop reason = %q, want stop_sequence", e.StopReason)
	}
	if e.Text == "" {
		t.Error("error event text is empty, want the API Error message")
	}
}

func TestParseSessionEventsNetworkError(t *testing.T) {
	// A network error never reached the server, so it carries no HTTP status
	// (apiErrorStatus absent → 0) and error="unknown"; it is still an error event.
	line := []byte(`{"type":"assistant","sessionId":"s","message":{"model":"<synthetic>","stop_reason":"stop_sequence","content":[{"type":"text","text":"API Error: The socket connection was closed unexpectedly"}]},"error":"unknown","isApiErrorMessage":true}`)
	events, err := ParseSessionEvents(line)
	if err != nil {
		t.Fatalf("ParseSessionEvents: %v", err)
	}
	if len(events) != 1 || events[0].Kind != EventError {
		t.Fatalf("events = %+v, want a single error event", events)
	}
	if events[0].ErrorStatus != 0 {
		t.Errorf("error status = %d, want 0 for a network error", events[0].ErrorStatus)
	}
}

func TestParseSessionEventsIgnoresBookkeeping(t *testing.T) {
	for _, line := range []string{
		``,
		`{"type":"user","message":{"content":[{"type":"tool_result","id":"tu1"}]}}`,
		`{"type":"mode","mode":"default","sessionId":"s"}`,
		`{"type":"system","subtype":"init","sessionId":"s"}`,
	} {
		events, err := ParseSessionEvents([]byte(line))
		if err != nil {
			t.Fatalf("ParseSessionEvents(%q): %v", line, err)
		}
		if len(events) != 0 {
			t.Errorf("ParseSessionEvents(%q) = %+v, want no events", line, events)
		}
	}
}

func TestParseSessionEventsToolUseStillEmittedWithoutEndTurn(t *testing.T) {
	// A mid-turn assistant entry (stop_reason tool_use) must not be reported as a turn end.
	line := []byte(`{"type":"assistant","message":{"stop_reason":"tool_use","content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}`)
	events, err := ParseSessionEvents(line)
	if err != nil {
		t.Fatalf("ParseSessionEvents: %v", err)
	}
	for _, e := range events {
		if e.Kind == EventTurnEnd {
			t.Fatalf("unexpected turn_end for mid-turn tool_use entry: %+v", events)
		}
	}
}
