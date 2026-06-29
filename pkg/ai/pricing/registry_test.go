package pricing

import (
	"math"
	"testing"
)

func TestCalculateCostBreaksOutBuckets(t *testing.T) {
	MergeModels(map[string]*ModelInfo{
		"test/bucketed": {
			ModelID:          "test/bucketed",
			InputPrice:       1,
			OutputPrice:      2,
			CacheReadsPrice:  0.25,
			CacheWritesPrice: 1.5,
		},
	})

	got, err := CalculateCost("test/bucketed", 1_000_000, 2_000_000, 500_000, 4_000_000, 2_000_000)
	if err != nil {
		t.Fatalf("CalculateCost: %v", err)
	}
	assertClose(t, "input", got.InputCost, 1)
	assertClose(t, "output", got.OutputCost, 4)
	assertClose(t, "reasoning", got.ReasoningCost, 1)
	assertClose(t, "cache read", got.CacheReadCost, 1)
	assertClose(t, "cache write", got.CacheWriteCost, 3)
	assertClose(t, "total", got.TotalCost, 10)
	if got.InputTokens != 1_000_000 || got.OutputTokens != 2_000_000 || got.ReasoningTokens != 500_000 || got.CacheReadTokens != 4_000_000 || got.CacheWriteTokens != 2_000_000 {
		t.Fatalf("tokens = %+v", got)
	}
}

func assertClose(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s cost = %v, want %v", name, got, want)
	}
}
