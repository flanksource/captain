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
	for i, path := range wf.Verify.Prompts {
		path = strings.TrimSpace(path)
		if path == "" {
			// Same rule as api.Workflow.Validate: a blank entry is a broken
			// declaration, and skipping it would silently drop a configured check
			// for callers that never ran Validate.
			return nil, fmt.Errorf("workflow.verify.prompts[%d] is empty", i)
		}
		tmpl, err := prompt.LoadFile(path)
		if err != nil {
			return nil, fmt.Errorf("verify prompt %q: %w", path, err)
		}
		if err := rejectJudgeOverrides(path, tmpl, provider); err != nil {
			return nil, err
		}
		hooks = append(hooks, New("judge:"+path, &LLMJudgeVerifier{Provider: provider, Prompt: tmpl}))
	}
	return hooks, nil
}

// rejectJudgeOverrides refuses judge frontmatter the hook cannot honour. A
// judge executes on the run's provider, so a declared sandbox — relocating or
// not — and a model different from the provider's would both be silently
// ignored, which is exactly the downgrade issue #39 forbids (R5.4: a hook
// prompt declaring a relocating sandbox is a validation error, never a silent
// fallback).
func rejectJudgeOverrides(path string, tmpl *prompt.Template, provider ai.Provider) error {
	probe, _, err := tmpl.Render(map[string]any{"cwd": "", "changed": []string{}}, nil)
	if err != nil {
		return fmt.Errorf("verify prompt %q: %w", path, err)
	}
	if probe.Sandbox != nil {
		return fmt.Errorf("verify prompt %q declares a sandbox; judge hooks run on the run's provider and cannot relocate", path)
	}
	if declared := strings.TrimSpace(probe.Model.Name); declared != "" && declared != provider.GetModel() {
		return fmt.Errorf("verify prompt %q declares model %q but judge hooks run on the run's provider (%s); remove the model or match it",
			path, declared, provider.GetModel())
	}
	return nil
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
