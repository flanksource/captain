package fixture

import "testing"

func TestResolveCost_UsesReportedWhenPresent(t *testing.T) {
	a := &aggregate{CostMean: 0.42, Input: 100, Output: 200}
	got, est := resolveCost("claude-sonnet-4-6", a)
	if got != 0.42 || est {
		t.Errorf("got %v, est=%v, want 0.42, false", got, est)
	}
}

func TestResolveCost_FallbackZeroWhenNoTokens(t *testing.T) {
	a := &aggregate{}
	got, est := resolveCost("claude-sonnet-4-6", a)
	if got != 0 || est {
		t.Errorf("got %v, est=%v, want 0, false", got, est)
	}
}

func TestResolveCost_FallbackComputesFromTokens(t *testing.T) {
	// claude-sonnet-4-6 pricing: $3/M input, $15/M output. Cache R/W: $0.30 / $3.75 per M.
	a := &aggregate{Input: 1_000_000, Output: 1_000_000}
	got, est := resolveCost("claude-sonnet-4-6", a)
	if !est {
		t.Fatal("estimated should be true on fallback")
	}
	want := 3.0 + 15.0
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestResolveCost_FallbackUnknownModelReturnsZero(t *testing.T) {
	a := &aggregate{Input: 100, Output: 100}
	got, est := resolveCost("definitely-not-a-real-model-zzz", a)
	if got != 0 || est {
		t.Errorf("got %v, est=%v, want 0, false (model unknown → no estimate)", got, est)
	}
}

func TestFormatCostWithEstimate(t *testing.T) {
	if got := formatCostWithEstimate(0, false); got != "$0" {
		t.Errorf("zero cost: got %q", got)
	}
	if got := formatCostWithEstimate(0.0234, false); got != "$0.0234" {
		t.Errorf("real cost: got %q", got)
	}
	if got := formatCostWithEstimate(0.0234, true); got != "$0.0234 (est)" {
		t.Errorf("estimated cost: got %q", got)
	}
	if got := formatCostWithEstimate(0, true); got != "$0" {
		t.Errorf("zero estimated should not show (est) suffix: got %q", got)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:                "0 B",
		512:              "512 B",
		1024:             "1.0 KB",
		1024 * 1024:      "1.0 MB",
		1536:             "1.5 KB",
		1024 * 1024 * 5:  "5.0 MB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
