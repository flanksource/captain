package api

import "testing"

func TestUsageTotalTokens(t *testing.T) {
	u := Usage{InputTokens: 10, OutputTokens: 20, ReasoningTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 2}
	if got := u.TotalTokens(); got != 40 {
		t.Errorf("TotalTokens() = %d, want 40", got)
	}
}

func TestCostAddAndTotal(t *testing.T) {
	a := Cost{
		Model:            "m1",
		InputTokens:      100,
		OutputTokens:     50,
		ReasoningTokens:  20,
		CacheReadTokens:  30,
		CacheWriteTokens: 10,
		TotalTokens:      210,
		InputCost:        0.10,
		OutputCost:       0.25,
		ReasoningCost:    0.03,
		CacheReadCost:    0.02,
		CacheWriteCost:   0.04,
	}
	b := Cost{
		Model:            "ignored",
		InputTokens:      10,
		OutputTokens:     5,
		ReasoningTokens:  2,
		CacheReadTokens:  3,
		CacheWriteTokens: 1,
		TotalTokens:      21,
		InputCost:        0.01,
		OutputCost:       0.02,
		ReasoningCost:    0.003,
		CacheReadCost:    0.002,
		CacheWriteCost:   0.004,
	}
	sum := a.Add(b)
	if sum.Model != "m1" {
		t.Errorf("Add keeps receiver Model, got %q", sum.Model)
	}
	if sum.InputTokens != 110 || sum.ReasoningTokens != 22 || sum.CacheReadTokens != 33 || sum.CacheWriteTokens != 11 || sum.TotalTokens != 231 {
		t.Errorf("Add tokens wrong: %+v", sum)
	}
	if got := sum.Total(); got < 0.478999 || got > 0.479001 {
		t.Errorf("Total() = %v, want 0.479", got)
	}
}

func TestCostsSumAndByModel(t *testing.T) {
	cs := Costs{
		{Model: "a", InputCost: 1, OutputCost: 1},
		{Model: "b", InputCost: 2, OutputCost: 0},
		{Model: "a", InputCost: 0.5, OutputCost: 0.5},
	}
	if got := cs.Sum().Total(); got != 5 {
		t.Errorf("Sum().Total() = %v, want 5", got)
	}
	byModel := cs.ByModel()
	if got := byModel["a"].Total(); got != 3 {
		t.Errorf("ByModel[a].Total() = %v, want 3", got)
	}
	if got := byModel["b"].Total(); got != 2 {
		t.Errorf("ByModel[b].Total() = %v, want 2", got)
	}
}
