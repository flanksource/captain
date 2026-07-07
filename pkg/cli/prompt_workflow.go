package cli

import (
	"strings"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/agent/verify"
	"github.com/flanksource/captain/pkg/api"
)

// workflowPlugins builds the generate→verify loop's plugins from a spec's
// Workflow: each verify command becomes a pass/fail CmdVerifier whose failure
// feedback drives a re-run. Returns nil when there is nothing to verify, in
// which case the loop is a plain single (or capped) generation.
func workflowPlugins(wf *api.Workflow) []agent.Plugin {
	if wf == nil || wf.Verify == nil {
		return nil
	}
	var plugins []agent.Plugin
	for _, cmd := range wf.Verify.Commands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		plugins = append(plugins, verify.New(
			"verify:"+cmd,
			&verify.CmdVerifier{Cmd: "sh", Args: []string{"-c", cmd}},
		))
	}
	return plugins
}

// workflowMaxIterations is the loop cap: the declared maxIterations, else 1 (a
// single generation, with verification voting once but no automatic re-run).
func workflowMaxIterations(wf *api.Workflow) int {
	if wf != nil && wf.Verify != nil && wf.Verify.MaxIterations > 0 {
		return wf.Verify.MaxIterations
	}
	return 1
}

// workflowScope maps the spec verify scope onto the runner scope (default all).
func workflowScope(wf *api.Workflow) agent.Scope {
	if wf != nil && wf.Verify != nil && wf.Verify.Scope == api.VerifyScopeChanged {
		return agent.ScopeChanged
	}
	return agent.ScopeAll
}

// verdictReason surfaces the last (failing) verifier reason in a run summary.
func verdictReason(verdicts []agent.Verdict) string {
	if len(verdicts) == 0 {
		return ""
	}
	return verdicts[len(verdicts)-1].Reason
}
