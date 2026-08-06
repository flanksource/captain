package adapter

import (
	"testing"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/gitagent"
)

func TestGitAgentRejectsInvalidWaitTimeout(t *testing.T) {
	for _, value := range []any{"fifteen", "0s", 15} {
		_, err := GitAgent(api.SandboxConfig{Options: map[string]any{"waitTimeout": value}})
		if err == nil {
			t.Fatalf("waitTimeout %#v was accepted", value)
		}
	}
}

func TestHookSetsJSONPreservesConfiguredTiers(t *testing.T) {
	data, err := hookSetsJSON(map[string]any{
		"hooks": map[string]any{
			"sidecar":    map[string]any{"verify": map[string]any{"commands": []any{"false"}}},
			"supervisor": map[string]any{"verify": map[string]any{"commands": []any{"make lint"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	hooks, err := gitagent.DecodeHookSets(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := hooks.Sidecar.Verify.Commands; len(got) != 1 || got[0] != "false" {
		t.Fatalf("sidecar commands = %#v", got)
	}
	if got := hooks.Supervisor.Verify.Commands; len(got) != 1 || got[0] != "make lint" {
		t.Fatalf("supervisor commands = %#v", got)
	}
}

func TestHookSetsJSONRejectsAWorkflowAtTheWrongLevel(t *testing.T) {
	_, err := hookSetsJSON(map[string]any{
		"hooks": map[string]any{"verify": map[string]any{"commands": []any{"false"}}},
	})
	if err == nil {
		t.Fatal("hooks.verify was accepted; hooks must name sidecar or supervisor")
	}
}
