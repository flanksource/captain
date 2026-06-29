package cmux

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai/pricing"
)

// assistantLine builds a session-log assistant entry with the given usage so the
// stats parser has realistic input without embedding opaque literals.
func assistantLine(ts, model string, in, out, cacheRead, cacheCreate int) string {
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"message":{"model":%q,"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d},"content":[{"type":"text","text":"hi"}]}}`,
		ts, model, in, out, cacheRead, cacheCreate,
	)
}

func TestComputeSessionStatsAggregatesUsage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	// Two assistant turns 30s apart plus a non-assistant line that must be ignored
	// for token accounting but still counts toward the time span.
	writeSessionLog(t, path,
		assistantLine("2026-06-23T10:00:00Z", "claude-opus-4-8", 100, 20, 5, 50),
		`{"type":"user","timestamp":"2026-06-23T10:00:10Z","message":{"content":[{"type":"tool_result"}]}}`,
		assistantLine("2026-06-23T10:00:30Z", "claude-opus-4-8", 200, 40, 7, 0),
	)

	stats, err := computeSessionStats(path)
	if err != nil {
		t.Fatalf("computeSessionStats() error = %v", err)
	}
	if !stats.Found {
		t.Fatal("Found = false, want true")
	}
	if stats.InputTokens != 300 || stats.OutputTokens != 60 {
		t.Fatalf("tokens = in:%d out:%d, want in:300 out:60", stats.InputTokens, stats.OutputTokens)
	}
	if stats.CacheReadTokens != 12 || stats.CacheCreationTokens != 50 {
		t.Fatalf("cache tokens = read:%d create:%d, want read:12 create:50", stats.CacheReadTokens, stats.CacheCreationTokens)
	}
	if stats.TotalTokens != 300+60+12+50 {
		t.Fatalf("TotalTokens = %d, want %d", stats.TotalTokens, 300+60+12+50)
	}
	// ContextTokens tracks the latest turn's window (input + cache), not the sum:
	// the second turn's 200 in + 7 cache-read + 0 cache-create.
	if stats.ContextTokens != 200+7+0 {
		t.Fatalf("ContextTokens = %d, want %d", stats.ContextTokens, 200+7+0)
	}
	if stats.Compactions != 0 {
		t.Fatalf("Compactions = %d, want 0", stats.Compactions)
	}
	if stats.Turns != 2 {
		t.Fatalf("Turns = %d, want 2", stats.Turns)
	}
	if stats.Model != "claude-opus-4-8" {
		t.Fatalf("Model = %q, want claude-opus-4-8", stats.Model)
	}
	if stats.DurationMs != 30_000 {
		t.Fatalf("DurationMs = %d, want 30000", stats.DurationMs)
	}
	if stats.InProgress {
		t.Fatal("cold stats must not be in progress")
	}
}

// compactionLine builds a `compact_boundary` system entry whose metadata reports
// the post-compaction context size, mirroring Claude's session-log marker.
func compactionLine(ts string, postTokens int) string {
	return fmt.Sprintf(
		`{"type":"system","subtype":"compact_boundary","timestamp":%q,"content":"Conversation compacted","compactMetadata":{"trigger":"auto","preTokens":199417,"postTokens":%d}}`,
		ts, postTokens,
	)
}

func TestComputeSessionStatsCountsCompactions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	// A turn fills the window, a compaction shrinks it to postTokens, then a final
	// turn grows it again: ContextTokens must track the latest turn, not the sum.
	writeSessionLog(t, path,
		assistantLine("2026-06-23T10:00:00Z", "claude-opus-4-8", 5000, 100, 180000, 0),
		compactionLine("2026-06-23T10:00:30Z", 12000),
		assistantLine("2026-06-23T10:01:00Z", "claude-opus-4-8", 800, 200, 13000, 0),
	)

	stats, err := computeSessionStats(path)
	if err != nil {
		t.Fatalf("computeSessionStats() error = %v", err)
	}
	if stats.Compactions != 1 {
		t.Fatalf("Compactions = %d, want 1", stats.Compactions)
	}
	if stats.ContextTokens != 800+13000+0 {
		t.Fatalf("ContextTokens = %d, want %d (latest turn's window)", stats.ContextTokens, 800+13000)
	}
	// The compaction line carries no usage, so it must not inflate token totals.
	if stats.Turns != 2 {
		t.Fatalf("Turns = %d, want 2 (compaction is not a turn)", stats.Turns)
	}
}

func TestComputeSessionStatsContextFromCompactionPostTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	// When a compaction is the most recent event (no turn follows yet), the context
	// window reflects the post-compaction size the boundary reports.
	writeSessionLog(t, path,
		assistantLine("2026-06-23T10:00:00Z", "claude-opus-4-8", 5000, 100, 180000, 0),
		compactionLine("2026-06-23T10:00:30Z", 12000),
	)

	stats, err := computeSessionStats(path)
	if err != nil {
		t.Fatalf("computeSessionStats() error = %v", err)
	}
	if stats.Compactions != 1 {
		t.Fatalf("Compactions = %d, want 1", stats.Compactions)
	}
	if stats.ContextTokens != 12000 {
		t.Fatalf("ContextTokens = %d, want 12000 (post-compaction size)", stats.ContextTokens)
	}
}

// apiErrorLine builds the synthetic assistant entry Claude Code records when an
// API request fails after retries: stop_reason "stop_sequence" plus the
// isApiErrorMessage marker, classification, and (for HTTP errors) status.
func apiErrorLine(ts, errType string, status int, text string) string {
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"message":{"model":"<synthetic>","stop_reason":"stop_sequence","content":[{"type":"text","text":%q}],"usage":{"input_tokens":0,"output_tokens":0}},"error":%q,"isApiErrorMessage":true,"apiErrorStatus":%d}`,
		ts, text, errType, status,
	)
}

func TestComputeSessionStatsDetectsAPIError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	// A normal turn, then an API error ends the session: the stop_sequence error
	// must surface as State=error (not completed) with its message.
	writeSessionLog(t, path,
		assistantContentLine("2026-06-23T10:00:00Z", "", `{"type":"text","text":"working"}`),
		apiErrorLine("2026-06-23T10:00:30Z", "server_error", 529, "API Error: 529 Overloaded"),
	)

	stats, err := computeSessionStats(path)
	if err != nil {
		t.Fatalf("computeSessionStats() error = %v", err)
	}
	if stats.State != sessionStateError {
		t.Fatalf("State = %q, want %q", stats.State, sessionStateError)
	}
	if stats.Error != "API Error: 529 Overloaded" {
		t.Fatalf("Error = %q, want the API error message", stats.Error)
	}
}

func TestComputeSessionStatsErrorClearsOnRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	// A transient error followed by a real end_turn (after the user re-prompted):
	// the latest event wins, so State is completed and Error is cleared.
	writeSessionLog(t, path,
		apiErrorLine("2026-06-23T10:00:00Z", "rate_limit", 429, "API Error: Rate limited"),
		assistantContentLine("2026-06-23T10:01:00Z", "end_turn", `{"type":"text","text":"done"}`),
	)

	stats, err := computeSessionStats(path)
	if err != nil {
		t.Fatalf("computeSessionStats() error = %v", err)
	}
	if stats.State != sessionStateCompleted {
		t.Fatalf("State = %q, want %q (latest event wins)", stats.State, sessionStateCompleted)
	}
	if stats.Error != "" {
		t.Fatalf("Error = %q, want empty after recovery", stats.Error)
	}
}

// assistantContentLine builds an assistant entry with raw content blocks and an
// optional stop reason, for exercising session-state derivation.
func assistantContentLine(ts, stopReason, content string) string {
	return fmt.Sprintf(
		`{"type":"assistant","timestamp":%q,"message":{"model":"claude-opus-4-8","stop_reason":%q,"content":[%s],"usage":{"input_tokens":1,"output_tokens":1}}}`,
		ts, stopReason, content,
	)
}

func TestComputeSessionStatsDerivesState(t *testing.T) {
	cases := []struct {
		name    string
		content string
		stop    string
		want    string
	}{
		{"thinking block", `{"type":"thinking","thinking":"hmm"}`, "", sessionStateThinking},
		{"running a tool", `{"type":"tool_use","name":"Edit","id":"t1","input":{}}`, "", sessionStateWorking},
		{"awaiting an answer", `{"type":"tool_use","name":"AskUserQuestion","id":"t2","input":{}}`, "", sessionStateAsk},
		{"turn ended", `{"type":"text","text":"done"}`, "end_turn", sessionStateCompleted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "s.jsonl")
			writeSessionLog(t, path, assistantContentLine("2026-06-23T10:00:00Z", tc.stop, tc.content))
			stats, err := computeSessionStats(path)
			if err != nil {
				t.Fatalf("computeSessionStats() error = %v", err)
			}
			if stats.State != tc.want {
				t.Fatalf("State = %q, want %q", stats.State, tc.want)
			}
		})
	}
}

func TestComputeSessionStatsStatePersistsAcrossToolResult(t *testing.T) {
	// A tool_use leaves the agent "working" until the next assistant turn; the
	// interleaved user/tool_result line carries no event and must not clear it.
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, path,
		assistantContentLine("2026-06-23T10:00:00Z", "", `{"type":"tool_use","name":"Bash","id":"t1","input":{}}`),
		`{"type":"user","timestamp":"2026-06-23T10:00:01Z","message":{"content":[{"type":"tool_result"}]}}`,
	)
	stats, err := computeSessionStats(path)
	if err != nil {
		t.Fatalf("computeSessionStats() error = %v", err)
	}
	if stats.State != sessionStateWorking {
		t.Fatalf("State = %q, want %q (tool_result must not clear working)", stats.State, sessionStateWorking)
	}
}

func TestSessionStatsExecuting(t *testing.T) {
	now := time.Date(2026, 6, 23, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		stats SessionStats
		want  bool
	}{
		{"not found ignores in-progress", SessionStats{Found: false, InProgress: true}, false},
		{"live tailer in progress", SessionStats{Found: true, InProgress: true}, true},
		{"cold working recently", SessionStats{Found: true, State: sessionStateWorking, UpdatedAt: now.Add(-10 * time.Second)}, true},
		{"cold thinking recently", SessionStats{Found: true, State: sessionStateThinking, UpdatedAt: now.Add(-1 * time.Second)}, true},
		{"cold awaiting input recently", SessionStats{Found: true, State: sessionStateAsk, UpdatedAt: now.Add(-5 * time.Second)}, true},
		{"cold working but stale", SessionStats{Found: true, State: sessionStateWorking, UpdatedAt: now.Add(-10 * time.Minute)}, false},
		{"cold completed", SessionStats{Found: true, State: sessionStateCompleted, UpdatedAt: now}, false},
		{"cold error", SessionStats{Found: true, State: sessionStateError, UpdatedAt: now}, false},
		{"cold active without timestamp", SessionStats{Found: true, State: sessionStateWorking}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.stats.Executing(now); got != tc.want {
				t.Fatalf("Executing() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNormalizeModelID(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8":            "claude-opus-4.8",
		"claude-sonnet-4-5-20250929": "claude-sonnet-4.5",
		"claude-haiku-4-5-20251001":  "claude-haiku-4.5",
		"claude-opus-4-1":            "claude-opus-4.1",
		"sonnet":                     "sonnet",                    // unversioned, unchanged
		"anthropic/claude-opus-4.8":  "anthropic/claude-opus-4.8", // already dotted, unchanged
	}
	for in, want := range cases {
		if got := normalizeModelID(in); got != want {
			t.Errorf("normalizeModelID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestComputeSessionStatsPricesHyphenatedModel(t *testing.T) {
	// Claude session logs carry hyphenated model ids ("claude-opus-4-8") that the
	// pricing registry keys under dotted OpenRouter ids; finalize must normalize so
	// the context window and cost resolve instead of silently reading zero.
	want, ok := pricing.GetModelInfo("anthropic/claude-opus-4.8")
	if !ok {
		t.Skip("pricing registry has no anthropic/claude-opus-4.8 entry")
	}
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, path, assistantLine("2026-06-23T10:00:00Z", "claude-opus-4-8", 1000, 500, 2000, 100))

	stats, err := computeSessionStats(path)
	if err != nil {
		t.Fatalf("computeSessionStats() error = %v", err)
	}
	if stats.ContextWindow != want.ContextWindow {
		t.Fatalf("ContextWindow = %d, want %d (from pricing registry)", stats.ContextWindow, want.ContextWindow)
	}
	if stats.CostUSD <= 0 {
		t.Fatalf("CostUSD = %v, want > 0 for a priced model", stats.CostUSD)
	}
}

func TestSessionStatsCacheMissingLogIsNotFound(t *testing.T) {
	c := NewSessionStatsCache()
	stats, err := c.Get("sess", filepath.Join(t.TempDir(), "missing.jsonl"))
	if err != nil {
		t.Fatalf("Get() error = %v, want nil for a missing log", err)
	}
	if stats.Found {
		t.Fatal("Found = true for a missing log, want false")
	}
}

func TestSessionStatsCacheColdCachesByMtime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, path, assistantLine("2026-06-23T10:00:00Z", "claude-opus-4-8", 100, 20, 0, 0))

	c := NewSessionStatsCache()
	first, err := c.Get("sess", path)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if first.OutputTokens != 20 {
		t.Fatalf("OutputTokens = %d, want 20", first.OutputTokens)
	}

	// Rewriting the log with a new mtime must invalidate the cold entry.
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	writeSessionLog(t, path,
		assistantLine("2026-06-23T10:00:00Z", "claude-opus-4-8", 100, 20, 0, 0),
		assistantLine("2026-06-23T10:00:05Z", "claude-opus-4-8", 100, 80, 0, 0),
	)
	second, err := c.Get("sess", path)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if second.OutputTokens != 100 {
		t.Fatalf("OutputTokens after rewrite = %d, want 100", second.OutputTokens)
	}
}

func TestSessionStatsCacheLivePreferredOverDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	writeSessionLog(t, path, assistantLine("2026-06-23T10:00:00Z", "claude-opus-4-8", 1, 1, 0, 0))

	c := NewSessionStatsCache()
	acc := c.Begin("sess", "claude", "opus", "high", time.Now())
	acc.AddLine([]byte(assistantLine("2026-06-23T10:00:00Z", "claude-opus-4-8", 100, 20, 0, 0)))

	live, err := c.Get("sess", path)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !live.InProgress {
		t.Fatal("live session InProgress = false, want true")
	}
	if live.Effort != "high" || live.Agent != "claude" {
		t.Fatalf("identity = agent:%q effort:%q, want claude/high", live.Agent, live.Effort)
	}
	// The live accumulator (out:20) wins over the on-disk line (out:1).
	if live.OutputTokens != 20 {
		t.Fatalf("OutputTokens = %d, want 20 (live, not disk)", live.OutputTokens)
	}

	acc.Finish()
	done, err := c.Get("sess", path)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if done.InProgress {
		t.Fatal("finished session InProgress = true, want false")
	}
}
