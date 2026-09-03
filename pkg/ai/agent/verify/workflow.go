package verify

import (
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// The workflow → hooks mapping itself lives in registry.go (HooksFor); what
// remains here is the loop shape a Workflow declares around those hooks.

// MaxIterationsForWorkflow is the loop cap: the declared maxIterations, else 1
// (a single generation; verification votes once with no automatic re-run).
func MaxIterationsForWorkflow(wf *api.Workflow) int {
	if wf != nil && wf.Verify != nil && wf.Verify.MaxIterations > 0 {
		return wf.Verify.MaxIterations
	}
	return 1
}

// ScopeForWorkflow maps the spec verify scope onto the runner scope (default all).
func ScopeForWorkflow(wf *api.Workflow) agent.Scope {
	if wf != nil && wf.Verify != nil && wf.Verify.Scope == api.VerifyScopeChanged {
		return agent.ScopeChanged
	}
	return agent.ScopeAll
}
