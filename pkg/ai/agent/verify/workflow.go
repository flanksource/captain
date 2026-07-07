package verify

import (
	"strings"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
)

// PluginsForWorkflow builds the generate→verify loop's plugins from a spec's
// Workflow: each verify command becomes a pass/fail CmdVerifier whose failure
// output drives a re-run. Returns nil when there is nothing to verify.
//
// Shared by captain prompt-run and gavel so both construct the loop identically
// from an api.Spec.Workflow instead of each hand-rolling verifiers.
func PluginsForWorkflow(wf *api.Workflow) []agent.Plugin {
	if wf == nil || wf.Verify == nil {
		return nil
	}
	var plugins []agent.Plugin
	for _, cmd := range wf.Verify.Commands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		plugins = append(plugins, New("verify:"+cmd, &CmdVerifier{Cmd: "sh", Args: []string{"-c", cmd}}))
	}
	return plugins
}

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
