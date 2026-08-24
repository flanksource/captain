package history

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude/tools"
)

func codexEventTurnID(event CodexEvent) string {
	if event.Payload.TurnID != "" {
		return event.Payload.TurnID
	}
	if event.Payload.Metadata != nil {
		return event.Payload.Metadata.TurnID
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func buildCodexEventUse(event CodexEvent, cwd, sessionID string) ToolUse {
	input := make(map[string]any, len(event.Payload.Raw)+1)
	for key, value := range event.Payload.Raw {
		input[key] = value
	}
	input["event"] = event.Payload.Type
	addCodexEventValue(input, "turn_id", event.Payload.TurnID)
	addCodexEventValue(input, "message", event.Payload.Message)
	addCodexEventValue(input, "phase", event.Payload.Phase)
	addCodexEventValue(input, "started_at", event.Payload.StartedAt)
	addCodexEventValue(input, "completed_at", event.Payload.CompletedAt)
	addCodexEventValue(input, "duration_ms", event.Payload.DurationMS)
	addCodexEventValue(input, "time_to_first_token_ms", event.Payload.TimeToFirstTokenMS)
	addCodexEventValue(input, "last_agent_message", event.Payload.LastAgentMessage)
	addCodexEventValue(input, "model_context_window", event.Payload.ModelContextWindow)
	addCodexEventValue(input, "collaboration_mode_kind", event.Payload.CollaborationModeKind)
	if event.Payload.Info != nil {
		last := event.Payload.Info.LastTokenUsage
		total := event.Payload.Info.TotalTokenUsage
		input["total_tokens"] = last.TotalTokens
		input["input_tokens"] = last.InputTokens
		input["output_tokens"] = last.OutputTokens
		input["cached_input_tokens"] = last.CachedInputTokens
		input["cumulative_total_tokens"] = total.TotalTokens
		input["cumulative_input_tokens"] = total.InputTokens
		input["cumulative_output_tokens"] = total.OutputTokens
		input["cumulative_cached_input_tokens"] = total.CachedInputTokens
		if last.ReasoningOutputTokens != 0 {
			input["reasoning_output_tokens"] = last.ReasoningOutputTokens
		}
		if event.Payload.Info.ModelContextWindow != 0 {
			input["model_context_window"] = event.Payload.Info.ModelContextWindow
		}
	}
	usage := eventTokenUsage(event)
	return ToolUse{
		Tool:            tools.EventToolName(event.Payload.Type),
		Input:           input,
		Timestamp:       event.Time(),
		CWD:             cwd,
		SessionID:       sessionID,
		TurnID:          codexEventTurnID(event),
		Source:          "codex",
		InputTokens:     codexNonCachedInputTokens(usage),
		OutputTokens:    api.NetOutputTokens(usage.OutputTokens, usage.ReasoningOutputTokens),
		ReasoningTokens: usage.ReasoningOutputTokens,
		CacheReadTokens: usage.CachedInputTokens,
		TotalTokens:     usage.TotalTokens,
		CumulativeUsage: cumulativeEventUsage(event),
		ContextWindow:   eventContextWindow(event),
		RecordType:      "event_msg." + event.Payload.Type,
	}
}

// cumulativeEventUsage nets the session-to-date totals codex reports alongside
// each event's own delta. Same netting as the per-event fields so the two are
// directly comparable: codex reports input inclusive of the cached prefix and
// output inclusive of reasoning.
func cumulativeEventUsage(event CodexEvent) *api.Usage {
	if event.Payload.Info == nil {
		return nil
	}
	total := event.Payload.Info.TotalTokenUsage
	if total == (CodexTokenUsage{}) {
		return nil
	}
	return &api.Usage{
		InputTokens:     codexNonCachedInputTokens(total),
		OutputTokens:    api.NetOutputTokens(total.OutputTokens, total.ReasoningOutputTokens),
		ReasoningTokens: total.ReasoningOutputTokens,
		CacheReadTokens: total.CachedInputTokens,
	}
}

func addCodexEventValue(input map[string]any, key string, value any) {
	switch typed := value.(type) {
	case string:
		if typed != "" {
			input[key] = typed
		}
	case int:
		if typed != 0 {
			input[key] = typed
		}
	case int64:
		if typed != 0 {
			input[key] = typed
		}
	case float64:
		if typed != 0 {
			input[key] = typed
		}
	}
}

func buildCodexTopLevelEventUse(event CodexEvent, eventType, cwd, sessionID string) ToolUse {
	input := make(map[string]any, len(event.Payload.Raw)+1)
	for key, value := range event.Payload.Raw {
		input[key] = value
	}
	input["event"] = eventType
	return ToolUse{
		Tool:       tools.EventToolName(eventType),
		Input:      input,
		Timestamp:  event.Time(),
		CWD:        cwd,
		SessionID:  sessionID,
		TurnID:     codexEventTurnID(event),
		Source:     "codex",
		RecordType: event.Type,
	}
}

func eventTokenUsage(event CodexEvent) CodexTokenUsage {
	if event.Payload.Info == nil {
		return CodexTokenUsage{}
	}
	return event.Payload.Info.LastTokenUsage
}

func eventContextWindow(event CodexEvent) int {
	if event.Payload.Info == nil {
		return 0
	}
	return event.Payload.Info.ModelContextWindow
}

func codexNonCachedInputTokens(usage CodexTokenUsage) int {
	if usage.CachedInputTokens <= 0 {
		return usage.InputTokens
	}
	input := usage.InputTokens - usage.CachedInputTokens
	if input < 0 {
		return usage.InputTokens
	}
	return input
}

// codexChatSlot is an emitted chat row that a twin may still merge into.
// families is the set of record families already folded in: Codex logs each
// logical message at most once per family, so a family arriving twice means a
// genuine repeat rather than a twin. Tracking the accumulated set rather than
// just the previous family is what keeps an (event_msg, response_item) pair
// repeated across N turns -- the same assistant verdict re-sent verbatim --
// as N rows instead of collapsing it to one.
type codexChatSlot struct {
	index    int
	priority int
	families map[string]struct{}
}

// codexDeduper folds each message's twin records into a single row while
// retaining only the unresolved output suffix. A live parser keeps this value
// across appends; rows before the earliest slot are final and can be released.
type codexDeduper struct {
	pending  []ToolUse
	previous map[string]*codexChatSlot
}

// push consumes one emitting source record and returns the prefix that can no
// longer be changed by a twin in a later record.
func (d *codexDeduper) push(record []ToolUse) []ToolUse {
	if len(record) == 0 {
		return nil
	}
	current := make(map[string]*codexChatSlot, len(record))
	for _, use := range record {
		key, ok := codexChatDedupeKey(use)
		if !ok {
			d.pending = append(d.pending, use)
			continue
		}
		if slot := mergeCodexTwin(d.pending, d.previous[key], use); slot != nil {
			current[key] = slot
			continue
		}
		d.pending = append(d.pending, use)
		current[key] = &codexChatSlot{
			index:    len(d.pending) - 1,
			priority: codexRecordPriority(use.RecordType),
			families: map[string]struct{}{codexRecordFamily(use.RecordType): {}},
		}
	}
	d.previous = current

	frontier := len(d.pending)
	for _, slot := range d.previous {
		if slot.index < frontier {
			frontier = slot.index
		}
	}
	if frontier == 0 {
		return nil
	}
	settled := d.pending[:frontier:frontier]
	if frontier == len(d.pending) {
		d.pending = nil
		return settled
	}
	d.pending = d.pending[frontier:]
	for _, slot := range d.previous {
		slot.index -= frontier
	}
	return settled
}

// snapshot returns the unresolved suffix as it should look at the current EOF.
// Only slots from the final emitting record are provisional; unrelated rows can
// be retained behind one for ordering without pretending their content may grow.
func (d *codexDeduper) snapshot() []ToolUse {
	output := append([]ToolUse(nil), d.pending...)
	for _, slot := range d.previous {
		output[slot.index].Provisional = true
	}
	return output
}

// mergeCodexTwin folds use into slot when slot holds the same message logged by
// a different record family, returning the slot merged into (nil when it did
// not). The higher-priority record wins the row, so response_item content and
// the model stamped on it survive whichever twin arrived first.
func mergeCodexTwin(output []ToolUse, slot *codexChatSlot, use ToolUse) *codexChatSlot {
	if slot == nil {
		return nil
	}
	family := codexRecordFamily(use.RecordType)
	if _, exists := slot.families[family]; exists {
		return nil
	}
	if priority := codexRecordPriority(use.RecordType); priority > slot.priority {
		firstLine := output[slot.index].SourceLine
		output[slot.index] = use
		output[slot.index].SourceLine = firstLine
		slot.priority = priority
	}
	slot.families[family] = struct{}{}
	return slot
}

// codexRecordFamily reduces a RecordType to the stream that logged it.
func codexRecordFamily(recordType string) string {
	switch {
	case strings.HasPrefix(recordType, "response_item."):
		return "response_item"
	case strings.HasPrefix(recordType, "item.completed"):
		return "item.completed"
	case strings.HasPrefix(recordType, "event_msg."):
		return "event_msg"
	}
	return recordType
}

func codexChatDedupeKey(use ToolUse) (string, bool) {
	switch use.Tool {
	case "System", "User", "Assistant", "Reasoning", "Plan", "MemoryCitation":
	default:
		return "", false
	}
	text := codexDedupeText(use)
	if text == "" {
		return "", false
	}
	// No timestamp: twins are written anywhere from the same millisecond to
	// hours apart (a resumed session re-logs its user message), so the only
	// reliable discriminators are content, adjacency, and record family.
	return strings.Join([]string{use.SessionID, use.Tool, text}, "\x00"), true
}

func codexDedupeText(use ToolUse) string {
	if text, ok := use.Input["text"].(string); ok {
		return strings.TrimSpace(text)
	}
	if content, ok := use.Input["content"].(string); ok {
		return strings.TrimSpace(content)
	}
	if use.Tool == "MemoryCitation" {
		return fmt.Sprint(use.Input["citation_entries"], "\x00", use.Input["rollout_ids"])
	}
	return ""
}

// codexRecordPriority ranks the families by authority: response_item carries
// the canonical content, so it wins the row over its event_msg twin.
func codexRecordPriority(recordType string) int {
	switch codexRecordFamily(recordType) {
	case "response_item":
		return 3
	case "item.completed":
		return 2
	case "event_msg":
		return 1
	default:
		return 0
	}
}
