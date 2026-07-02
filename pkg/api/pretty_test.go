package api

import (
	"strings"
	"testing"
)

func TestCostPretty(t *testing.T) {
	c := Cost{InputTokens: 1200, OutputTokens: 300, InputCost: 0.01, OutputCost: 0.02}
	got := c.Pretty().String()
	for _, want := range []string{"$0.0300", "1200 in", "300 out"} {
		if !strings.Contains(got, want) {
			t.Errorf("Cost.Pretty() = %q, want substring %q", got, want)
		}
	}
}

func TestSpecPretty(t *testing.T) {
	got := sampleSpec().Pretty().String()
	for _, want := range []string{"Spec", "claude-sonnet-4-6", "effort=xhigh", "mode=acceptEdits", "/repo"} {
		if !strings.Contains(got, want) {
			t.Errorf("Spec.Pretty() = %q, want substring %q", got, want)
		}
	}
}
