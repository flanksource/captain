package verify

import (
	"testing"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

func TestDeclaresExec(t *testing.T) {
	for _, test := range []struct {
		name string
		wf   *api.Workflow
		want bool
	}{
		{name: "nil workflow", wf: nil},
		{name: "no verify stage", wf: &api.Workflow{}},
		{name: "only blank commands", wf: &api.Workflow{Verify: &api.Verify{Commands: []string{"  "}}}},
		{name: "a command", wf: &api.Workflow{Verify: &api.Verify{Commands: []string{"go test ./..."}}}, want: true},
		{name: "a fixture", wf: &api.Workflow{Verify: &api.Verify{Fixture: "# acceptance"}}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := DeclaresExec(test.wf); got != test.want {
				t.Errorf("DeclaresExec = %t, want %t", got, test.want)
			}
		})
	}
}

func TestMaxIterationsAndScopeForWorkflow(t *testing.T) {
	if got := MaxIterationsForWorkflow(nil); got != 1 {
		t.Errorf("nil workflow → 1 iteration, got %d", got)
	}
	if got := ScopeForWorkflow(nil); got != agent.ScopeAll {
		t.Errorf("nil workflow → all scope, got %v", got)
	}

	wf := &api.Workflow{Verify: &api.Verify{MaxIterations: 5, Scope: api.VerifyScopeChanged}}
	if got := MaxIterationsForWorkflow(wf); got != 5 {
		t.Errorf("want 5 iterations, got %d", got)
	}
	if got := ScopeForWorkflow(wf); got != agent.ScopeChanged {
		t.Errorf("want changed scope, got %v", got)
	}
}
