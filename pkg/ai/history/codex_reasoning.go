package history

import (
	"fmt"
	"time"

	"github.com/segmentio/encoding/json"
)

// codexReasoningSummaryText returns the plaintext reasoning summary, if any.
// Codex ships the summary as an array of blocks; the last non-empty block wins.
func codexReasoningSummaryText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var summaries []CodexReasoningSummary
	if json.Unmarshal(raw, &summaries) != nil {
		return ""
	}
	var text string
	for _, summary := range summaries {
		if summary.Text != "" {
			text = summary.Text
		}
	}
	return text
}

// codexReasoningCollapser folds contentless reasoning records into one row per
// turn.
//
// Modern Codex emits reasoning as `summary:[]` plus `encrypted_content`: the
// text is unrecoverable, but the record still proves the session was alive at
// that instant. Dropping it understates the session's end time (and therefore
// last_activity_at) and inflates apparent idle gaps. Emitting one row per
// record would instead bury the transcript, so a turn's records collapse into a
// single row stamped with the LAST timestamp and carrying the span.
//
// Records that DO carry a plaintext summary keep their own row and never flush
// the span: plaintext and contentless reasoning mix within one turn.
type codexReasoningCollapser struct {
	turnID    string
	cwd       string
	sessionID string
	first     time.Time
	last      time.Time
	count     int
}

// observe consumes a response_item.reasoning event, returning any rows it
// completes. A turn change flushes the pending span before accumulating.
func (c *codexReasoningCollapser) observe(event CodexEvent, currentTurn, cwd, sessionID string) []ToolUse {
	if text := codexReasoningSummaryText(event.Payload.Summary); text != "" {
		return []ToolUse{{
			Tool:       "Reasoning",
			Input:      map[string]any{"text": text},
			Timestamp:  event.Time(),
			CWD:        cwd,
			SessionID:  sessionID,
			TurnID:     codexEventTurnID(event),
			Source:     "codex",
			RecordType: "response_item.reasoning",
		}}
	}

	ts := event.Time()
	if ts == nil {
		// No parseable timestamp is no evidence of when the session was alive,
		// which is the only thing a contentless record carries.
		return nil
	}

	var out []ToolUse
	turn := firstNonEmpty(codexEventTurnID(event), currentTurn)
	if c.count > 0 && c.turnID != turn {
		out = c.flush()
	}
	if c.count == 0 {
		c.turnID, c.cwd, c.sessionID = turn, cwd, sessionID
		c.first, c.last = *ts, *ts
	}
	if ts.Before(c.first) {
		c.first = *ts
	}
	if ts.After(c.last) {
		c.last = *ts
	}
	c.count++
	return out
}

// flush emits the pending span and resets. It returns nil when nothing is
// pending, so callers can flush unconditionally.
func (c *codexReasoningCollapser) flush() []ToolUse {
	if c.count == 0 {
		return nil
	}
	last := c.last
	use := ToolUse{
		Tool: "Reasoning",
		Input: map[string]any{
			"text":     codexReasoningSpanText(c.first, c.last, c.count),
			"first_at": c.first.UTC().Format(time.RFC3339Nano),
			"last_at":  c.last.UTC().Format(time.RFC3339Nano),
			"count":    c.count,
		},
		Timestamp:  &last,
		CWD:        c.cwd,
		SessionID:  c.sessionID,
		TurnID:     c.turnID,
		Source:     "codex",
		RecordType: "response_item.reasoning",
	}
	*c = codexReasoningCollapser{}
	return []ToolUse{use}
}

// codexReasoningSpanText renders the span. It must stay deterministic: the
// result feeds codexChatDedupeKey.
func codexReasoningSpanText(first, last time.Time, count int) string {
	if count == 1 {
		return fmt.Sprintf("1 encrypted reasoning record at %s", first.UTC().Format(time.RFC3339))
	}
	return fmt.Sprintf("%d encrypted reasoning records over %s (%s → %s)",
		count,
		last.Sub(first).Truncate(time.Second),
		first.UTC().Format(time.RFC3339),
		last.UTC().Format(time.RFC3339),
	)
}
