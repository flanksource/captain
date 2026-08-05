package session

import (
	"testing"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
)

// Codex reports both a per-record delta (last_token_usage) and the session's
// running total (total_token_usage). Only the running total is the provider's
// own answer; summing the deltas drifts away from it. Figures below are the
// shape observed on a real 238-event session where the deltas summed to
// 29,469,753 against a reported 29,236,689.
func tokenCountUse(model string, last api.Usage, cumulative api.Usage) history.ToolUse {
	return history.ToolUse{
		Tool:            "TokenCount",
		Source:          "codex",
		Model:           model,
		InputTokens:     last.InputTokens,
		OutputTokens:    last.OutputTokens,
		ReasoningTokens: last.ReasoningTokens,
		CacheReadTokens: last.CacheReadTokens,
		TotalTokens:     last.TotalTokens(),
		CumulativeUsage: &cumulative,
	}
}

func TestCodexCumulative_DeltasReconstructTheReportedTotal(t *testing.T) {
	var c codexCumulative
	// Deltas that do NOT add up to the reported totals — the real drift.
	uses := []history.ToolUse{
		tokenCountUse("gpt-5", api.Usage{InputTokens: 100, OutputTokens: 10}, api.Usage{InputTokens: 100, OutputTokens: 10}),
		tokenCountUse("gpt-5", api.Usage{InputTokens: 120, OutputTokens: 12}, api.Usage{InputTokens: 200, OutputTokens: 20}),
		tokenCountUse("gpt-5", api.Usage{InputTokens: 150, OutputTokens: 15}, api.Usage{InputTokens: 320, OutputTokens: 32}),
	}

	var summed api.Usage
	for _, u := range uses {
		delta, ok := c.delta(u)
		if !ok {
			t.Fatal("expected a cumulative figure on every token_count record")
		}
		summed.InputTokens += delta.InputTokens
		summed.OutputTokens += delta.OutputTokens
	}

	// The last cumulative is the answer; summing last_token_usage gives 370/37.
	if summed.InputTokens != 320 || summed.OutputTokens != 32 {
		t.Errorf("deltas summed to %d in / %d out, want the reported 320 / 32",
			summed.InputTokens, summed.OutputTokens)
	}
}

func TestCodexCumulative_TakesTheWholeTotalAfterACounterReset(t *testing.T) {
	var c codexCumulative
	if _, ok := c.delta(tokenCountUse("gpt-5", api.Usage{}, api.Usage{InputTokens: 500})); !ok {
		t.Fatal("expected a cumulative figure")
	}
	// A restarted session reports a smaller running total than before.
	delta, ok := c.delta(tokenCountUse("gpt-5", api.Usage{}, api.Usage{InputTokens: 40}))
	if !ok {
		t.Fatal("expected a cumulative figure")
	}
	if delta.InputTokens != 40 {
		t.Errorf("delta after reset = %d, want the cumulative 40 rather than a negative", delta.InputTokens)
	}
}

func TestCodexCumulative_ReportsAbsenceSoTheCallerCanFallBack(t *testing.T) {
	var c codexCumulative
	if _, ok := c.delta(history.ToolUse{Tool: "TokenCount", Model: "gpt-5"}); ok {
		t.Error("a record without a cumulative figure must report absence, not a zero delta")
	}
}

// Session usage must equal the last reported running total, not the sum of the
// per-record deltas — that is the whole point of reading the result.
func TestBuildCodexSession_UsesTheReportedRunningTotal(t *testing.T) {
	uses := []history.ToolUse{
		tokenCountUse("gpt-5",
			api.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 50},
			api.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 50}),
		tokenCountUse("gpt-5",
			api.Usage{InputTokens: 120, OutputTokens: 12, CacheReadTokens: 60},
			api.Usage{InputTokens: 200, OutputTokens: 20, CacheReadTokens: 95}),
	}
	for i := range uses {
		uses[i].SessionID = "codex-1"
	}

	s := buildCodexSession(uses, nil)

	if s.Usage.InputTokens != 200 || s.Usage.OutputTokens != 20 || s.Usage.CacheReadTokens != 95 {
		t.Errorf("session usage = %+v, want the reported total {200, 20, cacheRead 95}", s.Usage)
	}
	if s.Root.Usage != s.Usage {
		t.Errorf("root usage %+v disagrees with session usage %+v", s.Root.Usage, s.Usage)
	}
}
