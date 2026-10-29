package verify

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/hookaction"
)

// variables are what an after step's `if` and `with` read: the run's
// structured output and text, the prompt's vars and metadata, the outcome, and
// the outputs of the steps before this one.
func (h *AfterHook) variables(hc *agent.HookContext) (map[string]any, error) {
	output, err := hookaction.Native(hc.Response.StructuredData)
	if err != nil {
		return nil, fmt.Errorf("workflow.after[%d]: structured output: %w", h.Index, err)
	}
	steps, err := hookaction.Native(h.previousSteps())
	if err != nil {
		return nil, fmt.Errorf("workflow.after[%d]: earlier step outputs: %w", h.Index, err)
	}
	vars := map[string]any{}
	metadata := map[string]string{}
	if hc.Request != nil {
		maps.Copy(vars, hc.Request.Prompt.Variables)
		maps.Copy(metadata, hc.Request.Prompt.Metadata)
	}
	return map[string]any{
		"output": output, "result": hc.Response.Text, "vars": vars, "metadata": metadata,
		"outcome": map[string]bool{"failed": hc.Failed, "verified": hc.Verified}, "steps": steps,
	}, nil
}

func (h *AfterHook) previousSteps() []any {
	if h.steps == nil {
		return []any{}
	}
	return slices.Clone(*h.steps)
}

// runAction dispatches a tool or prompt step and reports it on the run's stream.
func (h *AfterHook) runAction(hc *agent.HookContext, vars map[string]any) (any, error) {
	kind, _ := h.Step.Kind()
	target := h.Step.Tool + h.Step.Prompt
	cwd := ""
	if kind == api.HookActionPrompt {
		dir, err := h.dir(hc)
		if err != nil {
			return nil, err
		}
		cwd = dir
	}
	out, err := hookaction.Run(hc, hookaction.Call{Action: h.Step.HookAction, Vars: vars, Cwd: cwd}, h.Opts.Actions)
	var skip *hookaction.Skip
	if kind == api.HookActionPrompt && errors.As(err, &skip) {
		hc.Notify("[post-run] %s skipped: %s", h.Name(), skip.Reason)
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workflow.after[%d] %s %q: %w", h.Index, kind, target, err)
	}
	hc.Notify("[post-run] %s %s %s done", h.Name(), kind, target)
	return out, nil
}
