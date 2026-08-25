package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/api"
)

func TestApplyObservationMetricsPreservesDisjointUsageAndProviderCost(t *testing.T) {
	result := api.RuntimeObservation{Metrics: api.ObservationMetrics{
		CostUSD: api.ObservationCostFact{State: api.ObservationFactUnknown, Unit: "USD"},
		Usage:   api.ObservationUsageFact{State: api.ObservationFactUnknown, Semantics: "disjoint-v1"},
	}}
	usage := api.Usage{
		InputTokens: 11, OutputTokens: 12, ReasoningTokens: 13,
		CacheReadTokens: 14, CacheWriteTokens: 15,
	}

	applyObservationMetrics(&result, api.BackendClaudeCLI, "claude-sonnet-5", &usage, 0.25)

	if result.Metrics.Usage.State != api.ObservationFactKnown || result.Metrics.Usage.Buckets == nil {
		t.Fatalf("usage fact = %#v, want known buckets", result.Metrics.Usage)
	}
	want := api.ObservationUsageBuckets{
		InputTokens: 11, OutputTokens: 12, ReasoningTokens: 13,
		CacheReadTokens: 14, CacheWriteTokens: 15,
	}
	if got := *result.Metrics.Usage.Buckets; got != want {
		t.Fatalf("usage buckets = %#v, want %#v", got, want)
	}
	if result.Metrics.CostUSD.State != api.ObservationFactKnown ||
		result.Metrics.CostUSD.Value == nil || *result.Metrics.CostUSD.Value != 0.25 ||
		result.Metrics.CostUSD.Source != "provider" {
		t.Fatalf("cost fact = %#v, want provider-reported 0.25", result.Metrics.CostUSD)
	}
}
