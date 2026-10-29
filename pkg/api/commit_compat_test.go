package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/flanksource/commons-db/workspace"
	"gopkg.in/yaml.v3"
)

// TestCommitGatesAcceptRetiredLevels: prompts written for the gates enum keep
// parsing, each level mapping onto the gate list it ran.
func TestCommitGatesAcceptRetiredLevels(t *testing.T) {
	cases := []struct {
		doc  string
		want CommitGateSpecs
	}{
		{doc: "gates: none", want: CommitGateSpecs{}},
		{doc: "gates: cheap", want: CommitGateSpecs{
			{Name: workspace.SecretNamesGateName},
			{Name: workspace.MaxSizeGateName, Config: map[string]any{"bytes": float64(1 << 20)}},
		}},
		{doc: "gates: full", want: CommitGateSpecs{{Name: FullPipelineGate}}},
		{doc: "gates: [{name: secret-names}, {name: max-size, config: {bytes: 64}}]", want: CommitGateSpecs{
			{Name: workspace.SecretNamesGateName},
			{Name: workspace.MaxSizeGateName, Config: map[string]any{"bytes": 64}},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.doc, func(t *testing.T) {
			var fromYAML Commit
			if err := yaml.Unmarshal([]byte(tc.doc), &fromYAML); err != nil {
				t.Fatalf("yaml: %v", err)
			}
			if !reflect.DeepEqual(fromYAML.Gates, tc.want) {
				t.Errorf("yaml gates = %#v, want %#v", fromYAML.Gates, tc.want)
			}
		})
	}

	var fromJSON Commit
	if err := json.Unmarshal([]byte(`{"gates":"cheap"}`), &fromJSON); err != nil {
		t.Fatalf("json level: %v", err)
	}
	if len(fromJSON.Gates) != 2 || fromJSON.Gates[0].Name != workspace.SecretNamesGateName {
		t.Errorf("json cheap = %#v, want the cheap gate list", fromJSON.Gates)
	}
	if err := json.Unmarshal([]byte(`{"gates":[{"name":"max-size","config":{"bytes":8}}]}`), &fromJSON); err != nil {
		t.Fatalf("json list: %v", err)
	}
	if !reflect.DeepEqual(fromJSON.Gates, CommitGateSpecs{{Name: workspace.MaxSizeGateName, Config: map[string]any{"bytes": float64(8)}}}) {
		t.Errorf("json list = %#v", fromJSON.Gates)
	}
}

func TestCommitGatesRejectUnknownLevel(t *testing.T) {
	var c Commit
	err := yaml.Unmarshal([]byte("gates: strict"), &c)
	if err == nil || !strings.Contains(err.Error(), `invalid commit gates "strict"`) {
		t.Fatalf("yaml.Unmarshal = %v, want an invalid-level error", err)
	}
}

// TestFullDirectiveIsNotAResolvableGate: the directive selects the host
// pipeline and must never reach workspace.ResolveGates, which would reject it.
func TestFullDirectiveIsNotAResolvableGate(t *testing.T) {
	gates := CommitGateSpecs{{Name: FullPipelineGate}, {Name: workspace.SecretNamesGateName}}
	if !HasGate(gates, FullPipelineGate) {
		t.Fatal("HasGate should see the full directive")
	}
	if got := gates.Resolvable(); !reflect.DeepEqual(got, []workspace.GateSpec{{Name: workspace.SecretNamesGateName}}) {
		t.Errorf("Resolvable() = %#v, want only secret-names", got)
	}
	if !HasGate(gates, FullPipelineGate) || len(gates) != 2 {
		t.Errorf("Resolvable must not mutate the policy's list, got %#v", gates)
	}
	wf := &Workflow{Commits: []Commit{{Gates: gates}}}
	if got := wf.CommitGates(); !reflect.DeepEqual(got, []workspace.GateSpec{{Name: workspace.SecretNamesGateName}}) {
		t.Errorf("Workflow.CommitGates() = %#v, want the directive stripped", got)
	}
}
