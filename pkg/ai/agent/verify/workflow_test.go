package verify

import (
	"testing"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

func TestPluginsForWorkflow(t *testing.T) {
	if PluginsForWorkflow(nil) != nil {
		t.Errorf("nil workflow should yield no plugins")
	}
	if PluginsForWorkflow(&api.Workflow{}) != nil {
		t.Errorf("workflow without verify should yield no plugins")
	}

	wf := &api.Workflow{Verify: &api.Verify{Commands: []string{"go test ./...", "  ", "go vet ./..."}}}
	plugins := PluginsForWorkflow(wf)
	if len(plugins) != 2 {
		t.Fatalf("want 2 plugins (blank command skipped), got %d", len(plugins))
	}
	if plugins[0].Name() != "verify:go test ./..." {
		t.Errorf("unexpected plugin name %q", plugins[0].Name())
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
