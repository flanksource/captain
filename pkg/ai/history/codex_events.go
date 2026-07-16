package history

import (
	"fmt"
	"strings"
	"time"

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
		OutputTokens:    usage.OutputTokens,
		CacheReadTokens: usage.CachedInputTokens,
		TotalTokens:     usage.TotalTokens,
		ContextWindow:   eventContextWindow(event),
		RecordType:      "event_msg." + event.Payload.Type,
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

func dedupeCodexToolUses(uses []ToolUse) []ToolUse {
	var output []ToolUse
	seen := map[string]int{}
	priorities := map[string]int{}
	for _, use := range uses {
		key, ok := codexChatDedupeKey(use)
		if !ok {
			output = append(output, use)
			continue
		}
		priority := codexRecordPriority(use.RecordType)
		if index, exists := seen[key]; exists {
			if priority > priorities[key] {
				output[index] = use
				priorities[key] = priority
			}
			continue
		}
		seen[key] = len(output)
		priorities[key] = priority
		output = append(output, use)
	}
	return output
}

func codexChatDedupeKey(use ToolUse) (string, bool) {
	switch use.Tool {
	case "System", "User", "Assistant", "Reasoning", "Plan", "MemoryCitation":
	default:
		return "", false
	}
	if use.Timestamp == nil {
		return "", false
	}
	text := codexDedupeText(use)
	if text == "" {
		return "", false
	}
	return strings.Join([]string{use.SessionID, use.Tool, use.Timestamp.UTC().Format(time.RFC3339Nano), text}, "\x00"), true
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

func codexRecordPriority(recordType string) int {
	switch {
	case strings.HasPrefix(recordType, "response_item."):
		return 3
	case strings.HasPrefix(recordType, "item.completed"):
		return 2
	case strings.HasPrefix(recordType, "event_msg."):
		return 1
	default:
		return 0
	}
}
