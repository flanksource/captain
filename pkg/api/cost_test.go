package api

import "testing"

func TestUsageTotalTokens(t *testing.T) {
	u := Usage{InputTokens: 10, OutputTokens: 20, ReasoningTokens: 5, CacheReadTokens: 3, CacheWriteTokens: 2}
	if got := u.TotalTokens(); got != 40 {
		t.Errorf("TotalTokens() = %d, want 40", got)
	}
}

func TestCostAddAndTotal(t *testing.T) {
	a := Cost{Model: "m1", InputTokens: 100, OutputTokens: 50, TotalTokens: 150, InputCost: 0.10, OutputCost: 0.25}
	b := Cost{Model: "ignored", InputTokens: 10, OutputTokens: 5, TotalTokens: 15, InputCost: 0.01, OutputCost: 0.02}
	sum := a.Add(b)
	if sum.Model != "m1" {
		t.Errorf("Add keeps receiver Model, got %q", sum.Model)
	}
	if sum.InputTokens != 110 || sum.TotalTokens != 165 {
		t.Errorf("Add tokens wrong: %+v", sum)
	}
	if got := sum.Total(); got < 0.379999 || got > 0.380001 {
		t.Errorf("Total() = %v, want 0.38", got)
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
