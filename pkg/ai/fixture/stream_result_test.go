package fixture

import "testing"

// resultLine is a verbatim `type: result` line from claude 2.1.220 — the same
// fixture pkg/ai/provider/claude_cli.go pins. The CLI reports the invocation's
// cost as total_cost_usd and its token totals at the top level, neither of which
// this parser used to read: cost came back 0 and tokens were rebuilt by summing
// per-message lines.
const resultLine = `{"is_error":false,"duration_api_ms":5,"num_turns":1,"stop_reason":"end_turn",` +
	`"session_id":"a32c3053-b70a-4f4f-9e0e-9d1d4fb48e8e","total_cost_usd":0.000162,` +
	`"usage":{"input_tokens":14,"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"output_tokens":8},` +
	`"terminal_reason":"completed","subtype":"success","result":"The capital of France is Paris.","type":"result"}`

func applyLines(t *testing.T, lines ...string) Summary {
	t.Helper()
	var summary Summary
	for _, line := range lines {
		ev, ok := ParseLine([]byte(line))
		if !ok {
			t.Fatalf("ParseLine(%q) returned not-ok", line)
		}
		summary.Apply(ev)
	}
	return summary
}

func TestSummaryReadsCostFromTheRealCLIResultKey(t *testing.T) {
	summary := applyLines(t, resultLine)

	if summary.CostUSD != 0.000162 {
		t.Errorf("CostUSD = %v, want 0.000162 from total_cost_usd", summary.CostUSD)
	}
}

func TestSummaryStillReadsTheAgentSDKCostKey(t *testing.T) {
	// The claude-agent SDK spells the same figure cost_usd on its turn-done
	// notification, so both producers must parse.
	summary := applyLines(t, `{"type":"result","subtype":"success","cost_usd":0.25,"result":"ok"}`)

	if summary.CostUSD != 0.25 {
		t.Errorf("CostUSD = %v, want 0.25 from cost_usd", summary.CostUSD)
	}
}

func TestSummaryTakesTokensFromTheResultTotal(t *testing.T) {
	summary := applyLines(t, resultLine)

	if summary.Input != 14 || summary.Output != 8 || summary.CacheRead != 3 || summary.CacheWrite != 2 {
		t.Errorf("usage = in:%d out:%d cacheRead:%d cacheWrite:%d, want 14/8/3/2",
			summary.Input, summary.Output, summary.CacheRead, summary.CacheWrite)
	}
	if !summary.UsageFromResult() {
		t.Error("UsageFromResult() = false, want true when the result reports usage")
	}
}

// The result's usage is the invocation total, so it replaces the per-message
// accumulation instead of adding to it — otherwise every turn is counted twice.
func TestSummaryResultTotalReplacesPerMessageAccumulation(t *testing.T) {
	assistant := `{"type":"assistant","message":{"id":"msg_a","usage":` +
		`{"input_tokens":14,"output_tokens":8,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}}`

	summary := applyLines(t, assistant, resultLine)

	if summary.Input != 14 || summary.Output != 8 {
		t.Errorf("usage = in:%d out:%d, want the reported total 14/8, not 28/16",
			summary.Input, summary.Output)
	}
}

// Without a result total the parser falls back to per-message usage, which must
// count a response once however many content-block lines it spans.
func TestSummaryFallsBackToDedupedPerMessageUsage(t *testing.T) {
	block := `{"type":"assistant","message":{"id":"msg_a","usage":` +
		`{"input_tokens":10,"output_tokens":4}}}`
	other := `{"type":"assistant","message":{"id":"msg_b","usage":` +
		`{"input_tokens":10,"output_tokens":4}}}`

	summary := applyLines(t, block, block, block, other)

	if summary.Input != 20 || summary.Output != 8 {
		t.Errorf("usage = in:%d out:%d, want 20/8 for two responses", summary.Input, summary.Output)
	}
	if summary.UsageFromResult() {
		t.Error("UsageFromResult() = true, want false when no result usage was reported")
	}
}
