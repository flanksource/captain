package hookaction

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

// Skip is an input hook's request not to run the prompt. It is also the error
// a child prompt's run returns when its own input hooks skip it, so a caller
// that dispatched the child can report the skip instead of a failure.
type Skip struct {
	Hook   string `json:"hook"`
	Reason string `json:"reason"`
}

func (s *Skip) Error() string { return fmt.Sprintf("skipped by %s: %s", s.Hook, s.Reason) }

// ApplyInputHooks runs spec's input.hooks in order. Each receives the run's
// vars and metadata (merged with its rendered `with`) and returns either
// {"vars": {...}}, which replaces Prompt.Variables for the hooks after it and
// the body render, or {"skip": "<reason>"}, which stops the chain and is
// returned. notify, when set, reports each hook skipped by its `if`.
func ApplyInputHooks(ctx context.Context, spec *api.Spec, runner Runner, notify func(string)) (*Skip, error) {
	for i, hook := range spec.Prompt.InputHooks {
		label := hookLabel("input.hooks", i, hook)
		vars := map[string]any{
			"vars": orEmpty(spec.Prompt.Variables), "metadata": stringMap(spec.Prompt.Metadata),
			"labels": stringMap(spec.Labels), "model": spec.Name,
		}
		run, err := hook.EvalIf(api.HookEnvInput, vars)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if !run {
			report(notify, fmt.Sprintf("[pre-run] %s skipped: if %s is false", label, hook.If))
			continue
		}
		base := map[string]any{"vars": vars["vars"], "metadata": vars["metadata"]}
		out, err := Run(ctx, Call{Action: hook, Vars: vars, Base: base, Cwd: spec.Cwd()}, runner)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		next, reason, err := inputHookResult(out)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", label, err)
		}
		if reason != "" {
			return &Skip{Hook: label, Reason: reason}, nil
		}
		spec.Prompt.Variables = next
	}
	return nil, nil
}

// inputHookResult decodes a hook's output: an object with exactly one of
// "vars" (an object) or "skip" (a non-empty reason).
func inputHookResult(out any) (map[string]any, string, error) {
	object, err := jsonObject(out)
	if err != nil {
		return nil, "", err
	}
	if len(object) != 1 || (object["vars"] == nil && object["skip"] == nil) {
		return nil, "", fmt.Errorf(`want an object with exactly one of "vars" or "skip", got keys %v`, slices.Sorted(maps.Keys(object)))
	}
	if raw, ok := object["skip"]; ok {
		reason, _ := raw.(string)
		if strings.TrimSpace(reason) == "" {
			return nil, "", fmt.Errorf(`"skip" must be a non-empty reason, got %v`, raw)
		}
		return nil, reason, nil
	}
	vars, ok := object["vars"].(map[string]any)
	if !ok {
		return nil, "", fmt.Errorf(`"vars" must be an object, got %T`, object["vars"])
	}
	return vars, "", nil
}
