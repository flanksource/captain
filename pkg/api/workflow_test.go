package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestWorkflowSpecRoundTrip(t *testing.T) {
	spec := Spec{
		Model:  Model{Name: "claude-sonnet-4-6"},
		Prompt: Prompt{User: "do the thing"},
		Workflow: &Workflow{
			Verify: &Verify{
				Commands:      []string{"go test ./...", "go vet ./..."},
				Fixture:       "- [ ] covers the edge case",
				Scope:         VerifyScopeChanged,
				MaxIterations: 3,
			},
			Commits:                  []Commit{{On: CommitOnTurn, Message: "apply"}},
			AutoVerifyWithoutFixture: true,
		},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Spec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Workflow == nil || got.Workflow.Verify == nil {
		t.Fatalf("workflow.verify lost in round-trip: %s", data)
	}
	if got.Workflow.Verify.MaxIterations != 3 || got.Workflow.Verify.Scope != VerifyScopeChanged {
		t.Errorf("verify fields not preserved: %+v", got.Workflow.Verify)
	}
	if got.Workflow.Verify.Fixture != spec.Workflow.Verify.Fixture || len(got.Workflow.Verify.Commands) != 2 {
		t.Errorf("verify commands/fixture not preserved: %+v", got.Workflow.Verify)
	}
	if len(got.Workflow.Commits) != 1 || got.Workflow.Commits[0].On != CommitOnTurn || got.Workflow.Commits[0].Message != "apply" {
		t.Errorf("commits not preserved: %+v", got.Workflow.Commits)
	}
	if !got.Workflow.AutoVerifyWithoutFixture {
		t.Errorf("autoVerifyWithoutFixture not preserved: %+v", got.Workflow)
	}
}

func TestWorkflowOmittedWhenNil(t *testing.T) {
	data, err := json.Marshal(Spec{Model: Model{Name: "m"}, Prompt: Prompt{User: "u"}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(data), "workflow") {
		t.Errorf("nil workflow should be omitted, got: %s", data)
	}
}

func TestSpecSchemaIncludesWorkflow(t *testing.T) {
	data, err := SchemaJSON(&Spec{})
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	for _, want := range []string{"workflow", "Workflow", "Verify", "commands", "maxIterations", "autoVerifyWithoutFixture"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("reflected spec schema missing %q", want)
		}
	}
	var reflected map[string]any
	if err := json.Unmarshal(data, &reflected); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	definitions, ok := reflected["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("reflected spec schema has no $defs object")
	}
	workflow, ok := definitions["Workflow"].(map[string]any)
	if !ok {
		t.Fatalf("reflected spec schema has no Workflow definition")
	}
	properties, ok := workflow["properties"].(map[string]any)
	if !ok {
		t.Fatalf("reflected Workflow schema has no properties object")
	}
	for _, gone := range []string{"output", "Output"} {
		if _, exists := properties[gone]; exists {
			t.Errorf("reflected Workflow schema still contains removed field %q", gone)
		}
	}
}

func TestVerifyScopeValidate(t *testing.T) {
	for _, ok := range []VerifyScope{"", VerifyScopeAll, VerifyScopeChanged} {
		if err := ok.Validate(); err != nil {
			t.Errorf("scope %q should be valid: %v", ok, err)
		}
	}
	if err := VerifyScope("sometimes").Validate(); err == nil {
		t.Errorf("unknown scope should fail validation")
	}
}
