package history

import "github.com/segmentio/encoding/json"

// SessionEventKind classifies a streamed event parsed from a Claude session log.
type SessionEventKind string

const (
	EventAssistantText SessionEventKind = "assistant"
	EventThinking      SessionEventKind = "thinking"
	EventToolUse       SessionEventKind = "tool_use"
	EventTurnEnd       SessionEventKind = "turn_end"
	// EventError is a turn that ended on an API/network error rather than a normal
	// completion (a synthetic `isApiErrorMessage` entry). It is terminal like
	// EventTurnEnd but signals failure, so callers can surface an error status
	// instead of treating the stop_sequence as success.
	EventError SessionEventKind = "error"
)

// SessionEvent is a single meaningful unit streamed from a Claude session log
// (`~/.claude/projects/<dir>/<id>.jsonl`). One log line (entry) can yield several
// events — e.g. an assistant entry with thinking + text + tool_use blocks.
type SessionEvent struct {
	Kind       SessionEventKind
	Text       string  // assistant/thinking content, or the error message when Kind == EventError
	ToolUse    ToolUse // populated when Kind == EventToolUse
	StopReason string  // populated when Kind == EventTurnEnd / EventError
	SessionID  string
	// ErrorType and ErrorStatus are populated when Kind == EventError: Claude
	// Code's error classification (e.g. "rate_limit", "server_error") and the HTTP
	// status (0 for a network/connection error that never reached the server).
	ErrorType   string
	ErrorStatus int
}

// ParseSessionEvents decodes a single Claude session-log line into the events it
// represents. Non-conversational lines (mode/attachment/system bookkeeping) and
// blank lines yield no events. A malformed JSON line returns an error so callers
// can decide whether to skip it.
func ParseSessionEvents(line []byte) ([]SessionEvent, error) {
	line = trimWhitespace(line)
	if len(line) == 0 {
		return nil, nil
	}
	var entry SessionEntry
	if err := json.Unmarshal(line, &entry); err != nil {
		return nil, err
	}
	return entry.Events(), nil
}

// Events converts a parsed session entry into streamed events. Only assistant
// entries carry conversational content; everything else is bookkeeping.
func (e SessionEntry) Events() []SessionEvent {
	if e.Type != "assistant" {
		return nil
	}

	// A synthetic API/network error entry ends the turn in failure. Surface it as a
	// single error event carrying the "API Error: …" message Claude Code recorded,
	// so callers reflect an error status rather than mis-reading its stop_sequence
	// as a normal completion.
	if e.IsAPIErrorMessage {
		return []SessionEvent{{
			Kind:        EventError,
			Text:        e.Message.firstText(),
			StopReason:  e.Message.StopReason,
			ErrorType:   e.ErrorType,
			ErrorStatus: e.APIErrorStatus,
			SessionID:   e.SessionID,
		}}
	}

	var events []SessionEvent
	for _, c := range e.Message.Content {
		switch c.Type {
		case "text":
			if c.Text != "" {
				events = append(events, SessionEvent{Kind: EventAssistantText, Text: c.Text, SessionID: e.SessionID})
			}
		case "thinking":
			if c.Thinking != "" {
				events = append(events, SessionEvent{Kind: EventThinking, Text: c.Thinking, SessionID: e.SessionID})
			}
		case "tool_use":
			events = append(events, SessionEvent{
				Kind:      EventToolUse,
				SessionID: e.SessionID,
				ToolUse: ToolUse{
					Tool:      c.Name,
					Input:     c.Input,
					ToolUseID: c.ID,
					SessionID: e.SessionID,
					CWD:       e.CWD,
					Source:    "claude",
				},
			})
		}
	}

	if isTerminalStopReason(e.Message.StopReason) {
		events = append(events, SessionEvent{Kind: EventTurnEnd, StopReason: e.Message.StopReason, SessionID: e.SessionID})
	}
	return events
}

// firstText returns the first non-empty text block of a message, used as the
// human-readable message for an error entry.
func (m Message) firstText() string {
	for _, c := range m.Content {
		if c.Type == "text" && c.Text != "" {
			return c.Text
		}
	}
	return ""
}

// isTerminalStopReason reports whether a stop_reason marks the end of an
// assistant turn (Claude is now idle / awaiting input). Intermediate tool_use
// turns and partial-stream entries (empty stop_reason) are not terminal.
func isTerminalStopReason(stopReason string) bool {
	switch stopReason {
	case "end_turn", "stop_sequence", "max_tokens":
		return true
	default:
		return false
	}
}
