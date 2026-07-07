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
				Scope:         VerifyScopeChanged,
				MaxIterations: 3,
				Gavel:         true,
			},
			Finalize: &Finalize{Commit: true, CommitMessage: "apply"},
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
	if !got.Workflow.Verify.Gavel || len(got.Workflow.Verify.Commands) != 2 {
		t.Errorf("verify commands/gavel not preserved: %+v", got.Workflow.Verify)
	}
	if got.Workflow.Finalize == nil || !got.Workflow.Finalize.Commit {
		t.Errorf("finalize not preserved: %+v", got.Workflow.Finalize)
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
	for _, want := range []string{"workflow", "Workflow", "Verify", "commands", "maxIterations"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("reflected spec schema missing %q", want)
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
