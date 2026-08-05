package session

import (
	"testing"

	"github.com/flanksource/captain/pkg/claude"
)

// blockLine builds one transcript line for a content block of an API response.
// Claude Code emits a separate line per block — thinking, text, tool_use — all
// carrying the same message id and the same usage object.
func blockLine(uuid, messageID string, usage *claude.Usage) claude.HistoryEntry {
	entry := assistantEntry(uuid, "", "claude-opus-5", usage, claude.ContentBlock{Type: claude.ContentTypeText, Text: "x"})
	entry.Message.ID = messageID
	return entry
}

// Taken from the real transcript of session 1e1612fa, where one response was
// written across three lines. Summing lines reported 4,918,567 cache-read
// tokens for a session that actually read 3,108,284 — a 58% overcount that
// inflated the reported cost from ~$4.30 to $6.72.
func oneResponseUsage() *claude.Usage {
	return &claude.Usage{
		InputTokens:              2,
		OutputTokens:             1188,
		CacheReadInputTokens:     110469,
		CacheCreationInputTokens: 1709,
	}
}

func TestResponseCosts_CountsOneResponseOncePerMessageID(t *testing.T) {
	costs := newResponseCosts()
	for _, uuid := range []string{"line-thinking", "line-text", "line-tooluse"} {
		costs.add(blockLine(uuid, "msg_011Cdhc1agCkXW7S5jiDU3RR", oneResponseUsage()))
	}

	total := costs.costs.Sum()
	want := oneResponseUsage()
	if got := total.CacheReadTokens; got != want.CacheReadInputTokens {
		t.Errorf("cache read tokens = %d, want %d (the three lines are one response, not three)",
			got, want.CacheReadInputTokens)
	}
	if got := total.OutputTokens; got != want.OutputTokens {
		t.Errorf("output tokens = %d, want %d", got, want.OutputTokens)
	}
	if got := len(costs.costs); got != 1 {
		t.Errorf("recorded %d costs, want 1", got)
	}
}

func TestResponseCosts_SumsDistinctResponses(t *testing.T) {
	costs := newResponseCosts()
	costs.add(blockLine("a-1", "msg_a", oneResponseUsage()))
	costs.add(blockLine("a-2", "msg_a", oneResponseUsage())) // same response, repeated line
	costs.add(blockLine("b-1", "msg_b", oneResponseUsage()))

	total := costs.costs.Sum()
	if want := 2 * oneResponseUsage().CacheReadInputTokens; total.CacheReadTokens != want {
		t.Errorf("cache read tokens = %d, want %d for two distinct responses", total.CacheReadTokens, want)
	}
}

// Entries without a message id cannot be correlated to a response, so each must
// still be counted rather than silently collapsing into one.
func TestResponseCosts_CountsEveryUnidentifiedEntry(t *testing.T) {
	costs := newResponseCosts()
	costs.add(blockLine("no-id-1", "", oneResponseUsage()))
	costs.add(blockLine("no-id-2", "", oneResponseUsage()))

	if got := len(costs.costs); got != 2 {
		t.Errorf("recorded %d costs, want 2 when no message id is available to dedupe on", got)
	}
}

func TestResponseCosts_IgnoresEntriesWithoutUsage(t *testing.T) {
	costs := newResponseCosts()
	costs.add(blockLine("no-usage", "msg_a", nil))

	if got := len(costs.costs); got != 0 {
		t.Errorf("recorded %d costs, want 0", got)
	}
}

// The turn rollup feeds monitor ingest (pkg/monitor/ingest.go writes one model
// call per turn from turn.Usage), so a turn that counts its content-block lines
// separately persists the inflation into the database.
func TestBuildSessionMetadata_CountsOneResponseOncePerTurn(t *testing.T) {
	usage := oneResponseUsage()
	entries := []claude.HistoryEntry{
		claudeTurnEntry("user-1", "2026-07-09T10:00:00Z", claude.MessageRoleUser, ""),
		blockLine("thinking", "msg_a", usage),
		blockLine("text", "msg_a", usage),
		stopEntry(blockLine("tooluse", "msg_a", usage)),
	}

	meta := buildSessionMetadata("claude", entries)

	if len(meta.turns) != 1 {
		t.Fatalf("built %d turns, want 1", len(meta.turns))
	}
	if got := meta.turns[0].Usage.CacheReadTokens; got != usage.CacheReadInputTokens {
		t.Errorf("turn cache read tokens = %d, want %d", got, usage.CacheReadInputTokens)
	}
	if got := meta.turns[0].Usage.OutputTokens; got != usage.OutputTokens {
		t.Errorf("turn output tokens = %d, want %d", got, usage.OutputTokens)
	}
}

func stopEntry(entry claude.HistoryEntry) claude.HistoryEntry {
	entry.Timestamp = "2026-07-09T10:00:05Z"
	entry.Message.StopReason = claude.StopReasonEndTurn
	return entry
}
