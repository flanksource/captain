package verify

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
)

// HooksForWorkflow builds the generate→verify loop's Verify hooks from a spec's
// Workflow: each verify command becomes a pass/fail CmdVerifier whose failure
// output drives a re-run. Returns nil when there is nothing to verify.
//
// Shared by captain prompt-run and gavel so both construct the loop identically
// from an api.Spec.Workflow. Returns []any — the runner's heterogeneous hook list.
func HooksForWorkflow(wf *api.Workflow) []any {
	if wf == nil || wf.Verify == nil {
		return nil
	}
	var hooks []any
	for _, cmd := range wf.Verify.Commands {
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		hooks = append(hooks, New("verify:"+cmd, &CmdVerifier{Cmd: "sh", Args: []string{"-c", cmd}}))
	}
	return hooks
}

// PromptHooksForWorkflow builds the LLM-judge hooks from Verify.Prompts: each
// entry is a .prompt template whose output schema is {ok, reason, feedback},
// judged by the given provider. It is separate from HooksForWorkflow because
// judge hooks need a provider and command hooks do not — keeping the original
// signature stable for gavel, which shares it.
//
// A prompt that fails to load is an error, not a skipped hook: a declared
// check that silently never runs is a false accept.
func PromptHooksForWorkflow(wf *api.Workflow, provider ai.Provider) ([]any, error) {
	if wf == nil || wf.Verify == nil || len(wf.Verify.Prompts) == 0 {
		return nil, nil
	}
	if provider == nil {
		return nil, fmt.Errorf("verify prompts declared but no provider available to judge them")
	}
	var hooks []any
	for _, path := range wf.Verify.Prompts {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		tmpl, err := prompt.LoadFile(path)
		if err != nil {
			return nil, fmt.Errorf("verify prompt %q: %w", path, err)
		}
		hooks = append(hooks, New("judge:"+path, &LLMJudgeVerifier{Provider: provider, Prompt: tmpl}))
	}
	return hooks, nil
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
