package verify

import (
	"testing"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

func TestHooksForWorkflow(t *testing.T) {
	if HooksForWorkflow(nil) != nil {
		t.Errorf("nil workflow should yield no hooks")
	}
	if HooksForWorkflow(&api.Workflow{}) != nil {
		t.Errorf("workflow without verify should yield no hooks")
	}

	wf := &api.Workflow{Verify: &api.Verify{Commands: []string{"go test ./...", "  ", "go vet ./..."}}}
	hooks := HooksForWorkflow(wf)
	if len(hooks) != 2 {
		t.Fatalf("want 2 hooks (blank command skipped), got %d", len(hooks))
	}
	named, ok := hooks[0].(interface{ Name() string })
	if !ok || named.Name() != "verify:go test ./..." {
		t.Errorf("unexpected first hook %v", hooks[0])
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
