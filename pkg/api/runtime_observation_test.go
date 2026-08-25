package api

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestRuntimeObservationJSONPreservesZeroUsageBuckets(t *testing.T) {
	zero := int64(0)
	observation := RuntimeObservation{
		SchemaVersion: ObservationSchemaV1,
		ObservationID: "observation-1",
		Execution:     ObservationExecution{State: "completed", DurationMS: &zero},
		Metrics: ObservationMetrics{
			DurationMS: ObservationNumberFact{State: ObservationFactKnown, Value: &zero, Unit: "ms"},
			CostUSD:    ObservationCostFact{State: ObservationFactUnknown, Unit: "USD"},
			Usage: ObservationUsageFact{
				State: ObservationFactKnown, Semantics: "disjoint-v1",
				Buckets: &ObservationUsageBuckets{},
			},
		},
		Artifacts: []ObservationArtifact{},
	}

	data, err := json.Marshal(observation)
	if err != nil {
		t.Fatalf("marshal observation: %v", err)
	}
	for _, field := range [][]byte{
		[]byte(`"inputTokens":0`),
		[]byte(`"outputTokens":0`),
		[]byte(`"reasoningTokens":0`),
		[]byte(`"cacheReadTokens":0`),
		[]byte(`"cacheWriteTokens":0`),
		[]byte(`"artifacts":[]`),
	} {
		if !bytes.Contains(data, field) {
			t.Fatalf("observation JSON %s does not contain %s", data, field)
		}
	}
	if bytes.Contains(data, []byte(`"passed"`)) {
		t.Fatalf("captain observation must not declare conformance: %s", data)
	}
}
