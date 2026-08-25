package cli

import (
	"context"
	"testing"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/observation"
	"github.com/flanksource/captain/pkg/api"
)

type observationUsageProvider struct {
	usage *api.Usage
}

func (p observationUsageProvider) GetModel() string       { return "test-model" }
func (p observationUsageProvider) GetBackend() ai.Backend { return ai.BackendOpenAI }

func (p observationUsageProvider) Execute(ctx context.Context, _ ai.Request) (*ai.Response, error) {
	observation.RecordUsage(ctx, p.usage)
	return &ai.Response{Model: p.GetModel()}, nil
}

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

func TestExecuteObservationProviderDistinguishesMissingFromZeroUsage(t *testing.T) {
	tests := []struct {
		name      string
		usage     *api.Usage
		wantState api.ObservationFactState
	}{
		{name: "omitted", usage: nil, wantState: api.ObservationFactUnknown},
		{name: "present zero", usage: &api.Usage{}, wantState: api.ObservationFactKnown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := observation.NewRecorder()
			ctx := observation.ContextWithRecorder(context.Background(), recorder)
			run := executeObservationProvider(ctx, observationUsageProvider{usage: test.usage}, ai.Request{}, false, recorder)
			result := api.RuntimeObservation{Metrics: api.ObservationMetrics{
				Usage: api.ObservationUsageFact{State: api.ObservationFactUnknown, Semantics: "disjoint-v1"},
			}}
			applyObservationMetrics(&result, api.BackendOpenAI, "test-model", run.usage, 0)

			if result.Metrics.Usage.State != test.wantState {
				t.Fatalf("usage state = %q, want %q", result.Metrics.Usage.State, test.wantState)
			}
			if test.usage == nil {
				if result.Metrics.Usage.Buckets != nil {
					t.Fatalf("omitted usage buckets = %#v, want nil", result.Metrics.Usage.Buckets)
				}
				return
			}
			if result.Metrics.Usage.Buckets == nil || *result.Metrics.Usage.Buckets != (api.ObservationUsageBuckets{}) {
				t.Fatalf("known-zero usage buckets = %#v, want all five zero buckets", result.Metrics.Usage.Buckets)
			}
		})
	}
}
