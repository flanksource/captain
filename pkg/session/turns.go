package session

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/assistanttags"
	"github.com/flanksource/captain/pkg/claude"
)

const (
	claudeContextWindow = 1_000_000
	codexContextWindow  = 200_000
)

type sessionMetadataBuild struct {
	events       []Event
	capabilities Capabilities
	budget       *Budget
	turns        []Turn
	turnByEntry  map[string]string
}

func buildSessionMetadata(source string, entries []claude.HistoryEntry) sessionMetadataBuild {
	b := sessionMetadataBuild{turnByEntry: map[string]string{}}
	var current *Turn
	var latestTurnTime *time.Time
	var turnSeq int
	seenEntries := map[string]struct{}{}

	observeTurnTime := func(turn *Turn, ts *time.Time) {
		if turn == nil || ts == nil {
			return
		}
		if turn.StartedAt == nil || ts.Before(*turn.StartedAt) {
			turn.StartedAt = cloneTime(ts)
		}
		if latestTurnTime == nil || ts.After(*latestTurnTime) {
			latestTurnTime = cloneTime(ts)
		}
	}

	startTurn := func(ts *time.Time) *Turn {
		if current != nil {
			observeTurnTime(current, ts)
			return current
		}
		turnSeq++
		current = &Turn{
			ID:    fmt.Sprintf("turn-%d", turnSeq),
			Index: turnSeq,
		}
		observeTurnTime(current, ts)
		return current
	}
	finishTurn := func(ts *time.Time) {
		if current == nil {
			return
		}
		observeTurnTime(current, ts)
		if ts != nil {
			current.EndedAt = cloneTime(latestTurnTime)
		}
		if current.Context == nil {
			setTurnContext(current, source)
		}
		b.turns = append(b.turns, *current)
		current = nil
		latestTurnTime = nil
	}

	for _, entry := range entries {
		// Claude can append a previously recorded branch to the same JSONL file.
		// UUID is the durable entry identity, so replaying it must not create a
		// second turn boundary or charge the entry to a later turn.
		if entry.UUID != "" {
			if _, ok := seenEntries[entry.UUID]; ok {
				continue
			}
			seenEntries[entry.UUID] = struct{}{}
		}
		ts := entryTime(entry)
		if entry.Event != nil {
			ev := eventFromEntry(entry)
			switch entry.Event.Scope {
			case "turn":
				turn := startTurn(ts)
				ev.TurnID = turn.ID
				turn.Events = append(turn.Events, ev)
				if entry.Event.Type == "budget_usd" {
					budget := budgetFromData(entry.Event.Data, ts)
					if budget != nil {
						turn.Budget = budget
						b.budget = budget
					}
				}
			default:
				b.events = append(b.events, ev)
				mergeCapabilities(&b.capabilities, entry.Event)
				if entry.Event.Type == "budget_usd" {
					budget := budgetFromData(entry.Event.Data, ts)
					if budget != nil {
						b.budget = budget
					}
				}
			}
			continue
		}

		if entry.Message.Role == "" && len(entry.Message.Content) == 0 {
			continue
		}
		turn := startTurn(ts)
		if entry.UUID != "" {
			b.turnByEntry[entry.UUID] = turn.ID
			turn.MessageIDs = appendUnique(turn.MessageIDs, entry.UUID)
		}
		if entry.Message.Model != "" {
			turn.Model = entry.Message.Model
		}
		if entry.IsAssistantMessage() {
			for _, block := range entry.Message.Content {
				if block.Type != claude.ContentTypeText {
					continue
				}
				for _, segment := range assistanttags.Parse(block.Text) {
					if segment.Kind != assistanttags.SegmentMemoryCitation || segment.Citation == nil {
						continue
					}
					b.events = append(b.events, Event{
						Type:      "memory_citation",
						Scope:     "session",
						TurnID:    turn.ID,
						Timestamp: cloneTime(ts),
						UUID:      entry.UUID,
						Data: map[string]any{
							"source":           "claude",
							"citation_entries": segment.Citation.CitationEntries,
							"rollout_ids":      segment.Citation.RolloutIDs,
						},
					})
				}
			}
		}
		if entry.IsAssistantMessage() && entry.Message.Usage != nil {
			cost := CostFromUsage(entry.Message.Usage, entry.Message.Model)
			turn.Cost = turn.Cost.Add(cost)
			turn.Usage = usageFromCost(turn.Cost)
			turn.Context = contextFromUsage(entry.Message.Usage, source)
		}
		if entry.Message.StopReason != "" && entry.Message.StopReason != claude.StopReasonToolUse {
			turn.StopReason = string(entry.Message.StopReason)
			finishTurn(ts)
		}
	}
	finishTurn(nil)

	b.capabilities.Tools = sortedStrings(b.capabilities.Tools)
	b.capabilities.PendingMCPServers = sortedStrings(b.capabilities.PendingMCPServers)
	b.capabilities.Agents = sortedStrings(b.capabilities.Agents)
	b.capabilities.Skills = sortedStrings(b.capabilities.Skills)
	return b
}

func eventFromEntry(entry claude.HistoryEntry) Event {
	ev := Event{
		Type:  entry.Event.Type,
		Scope: entry.Event.Scope,
		UUID:  entry.UUID,
		Data:  entry.Event.Data,
	}
	if ts := entryTime(entry); ts != nil {
		ev.Timestamp = cloneTime(ts)
	}
	return ev
}

func latestContext(turns []Turn) *Context {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i].Context != nil {
			return turns[i].Context
		}
	}
	return nil
}

func entryTime(entry claude.HistoryEntry) *time.Time {
	if ts, err := entry.ParseTimestamp(); err == nil && !ts.IsZero() {
		return &ts
	}
	return nil
}

func cloneTime(ts *time.Time) *time.Time {
	if ts == nil {
		return nil
	}
	v := *ts
	return &v
}

func contextFromUsage(usage *claude.Usage, source string) *Context {
	if usage == nil {
		return nil
	}
	used := usage.InputTokens + usage.CacheReadInputTokens + usage.CacheCreationInputTokens
	window := contextWindow(source)
	return &Context{
		UsedTokens:   used,
		WindowTokens: window,
		FreePercent:  freeContextPercent(used, window),
	}
}

func setTurnContext(turn *Turn, source string) {
	if turn == nil || turn.Context != nil {
		return
	}
	if turn.Usage.TotalTokens() == 0 {
		return
	}
	used := turn.Usage.InputTokens + turn.Usage.CacheReadTokens + turn.Usage.CacheWriteTokens
	window := contextWindow(source)
	turn.Context = &Context{
		UsedTokens:   used,
		WindowTokens: window,
		FreePercent:  freeContextPercent(used, window),
	}
}

func budgetFromData(data map[string]any, ts *time.Time) *Budget {
	if len(data) == 0 {
		return nil
	}
	b := &Budget{
		Used:      floatFromAny(data["used"]),
		Total:     floatFromAny(data["total"]),
		Remaining: floatFromAny(data["remaining"]),
	}
	if ts != nil {
		b.UpdatedAt = cloneTime(ts)
	}
	if b.Used == 0 && b.Total == 0 && b.Remaining == 0 {
		return nil
	}
	return b
}

func mergeCapabilities(c *Capabilities, event *claude.TranscriptEvent) {
	if event == nil {
		return
	}
	switch event.Type {
	case "deferred_tools_delta":
		c.Tools = append(c.Tools, stringsFromAny(event.Data["addedNames"])...)
		c.PendingMCPServers = append(c.PendingMCPServers, stringsFromAny(event.Data["pendingMcpServers"])...)
	case "agent_listing_delta":
		c.Agents = append(c.Agents, stringsFromAny(event.Data["addedTypes"])...)
	case "skill_listing":
		c.Skills = append(c.Skills, stringsFromAny(event.Data["names"])...)
		if len(c.Skills) == 0 {
			c.Skills = append(c.Skills, skillNamesFromContent(stringFromAny(event.Data["content"]))...)
		}
	}
}

func stringsFromAny(value any) []string {
	switch v := value.(type) {
	case []string:
		return append([]string(nil), v...)
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			if s := stringFromAny(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if v == "" {
			return nil
		}
		return []string{v}
	default:
		return nil
	}
}

func skillNamesFromContent(content string) []string {
	var out []string
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		name := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if i := strings.Index(name, ":"); i >= 0 {
			name = strings.TrimSpace(name[:i])
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

func stringFromAny(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	default:
		return ""
	}
}

func floatFromAny(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case float32:
		return float64(v)
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case jsonNumber:
		f, _ := v.Float64()
		return f
	default:
		return 0
	}
}

type jsonNumber interface {
	Float64() (float64, error)
}

func appendUnique(in []string, value string) []string {
	if value == "" {
		return in
	}
	for _, existing := range in {
		if existing == value {
			return in
		}
	}
	return append(in, value)
}

func sortedStrings(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// ContextWindow returns the default context window used for list/detail
// occupancy calculations for a source.
func ContextWindow(source string) int {
	return contextWindow(source)
}

func contextWindow(source string) int {
	if source == "codex" {
		return codexContextWindow
	}
	return claudeContextWindow
}

func freeContextPercent(used, window int) int {
	if window <= 0 {
		return 0
	}
	if used < 0 {
		used = 0
	}
	free := 100 - int(float64(used)/float64(window)*100)
	if free < 0 {
		return 0
	}
	if free > 100 {
		return 100
	}
	return free
}
