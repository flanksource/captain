package promptcontext

import (
	"context"
	"fmt"
	"maps"
	"sort"
	"strings"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/api"
)

// firing is one hook that matched, with its output and the span it matched
// (nil for a hook with no pattern).
type firing struct {
	hook   Hook
	output string
	span   []int
}

// Apply folds every matching hook into spec.Prompt.User and records the applied
// names under Prompt.Metadata[MetadataKey].
//
// It first strips context blocks an earlier Apply left, so re-applying (a
// resumed run, a preview followed by the run) yields the same prompt, and every
// hook matches against that stripped prompt, so no hook ever matches another's
// output. A spec no hook applies to, and that carries no earlier context, is
// left untouched. A predicate or render that fails, or renders nothing, is an
// error naming the hook.
func Apply(ctx context.Context, spec *api.Spec, hooks []Hook) (Applied, error) {
	if spec == nil {
		return nil, fmt.Errorf("promptcontext: Apply needs a spec")
	}
	authored := blocks.Strip(spec.Prompt.User)
	vars := variables(*spec, authored)
	var fired []firing
	var applied Applied
	for _, hook := range hooks {
		f, ok, err := hook.fire(ctx, authored, vars)
		if err != nil {
			return nil, fmt.Errorf("context hook %s (%s): %w", hook.Name, hook.Source, err)
		}
		if ok {
			fired = append(fired, f)
			applied = append(applied, AppliedHook{Name: hook.Name, Source: hook.Source})
		}
	}
	_, recorded := spec.Prompt.Metadata[MetadataKey]
	if len(fired) == 0 && !recorded && len(blocks.Find(spec.Prompt.User)) == 0 {
		return nil, nil
	}
	user, err := compose(authored, fired)
	if err != nil {
		return nil, err
	}
	spec.Prompt.User = user
	spec.Prompt.Metadata = recordApplied(spec.Prompt.Metadata, applied)
	return applied, nil
}

func variables(spec api.Spec, prompt string) map[string]any {
	return map[string]any{
		VarPrompt: prompt, VarSource: spec.Prompt.Source, VarModel: spec.Name, VarCwd: spec.Cwd(),
		VarMetadata: nonNil(spec.Prompt.Metadata), VarLabels: nonNil(spec.Labels), VarCaptures: map[string]string{},
	}
}

func nonNil(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// fire runs one hook against the stripped prompt: match, then predicate, then
// render. ok is false when the hook does not apply.
func (h Hook) fire(ctx context.Context, prompt string, vars map[string]any) (f firing, ok bool, err error) {
	captures := map[string]string{}
	if h.match != nil {
		loc := h.match.FindStringSubmatchIndex(prompt)
		if loc == nil {
			return firing{}, false, nil
		}
		f.span = loc[:2]
		for i, group := range h.match.SubexpNames() {
			if group != "" && loc[2*i] >= 0 {
				captures[group] = prompt[loc[2*i]:loc[2*i+1]]
			} else if group != "" {
				captures[group] = ""
			}
		}
	}
	data := maps.Clone(vars)
	data[VarCaptures] = captures
	if h.when != nil {
		if ok, err := evalWhen(h.when, h.expr, data); err != nil || !ok {
			return firing{}, false, err
		}
	}
	for group, value := range captures {
		data[group] = value
	}
	output, err := h.render(ctx, data)
	if err != nil {
		return firing{}, false, err
	}
	if f.output = strings.TrimSpace(output); f.output == "" {
		return firing{}, false, fmt.Errorf("returned empty output")
	}
	f.hook = h
	return f, true, nil
}

// compose substitutes matched spans in prompt, then wraps it in the prepended
// and appended blocks, in hook order.
func compose(prompt string, fired []firing) (string, error) {
	var before, after []string
	var subs []firing
	for _, f := range fired {
		switch f.hook.Mode {
		case promptlib.HookPrepend:
			before = append(before, blocks.Wrap(f.hook.Name, f.output))
		case promptlib.HookAppend:
			after = append(after, blocks.Wrap(f.hook.Name, f.output))
		case promptlib.HookSubstitute:
			subs = append(subs, f)
		default:
			return "", fmt.Errorf("context hook %s (%s): unsupported mode %q", f.hook.Name, f.hook.Source, f.hook.Mode)
		}
	}
	body, err := substitute(prompt, subs)
	if err != nil {
		return "", err
	}
	parts := before
	if body != "" {
		parts = append(parts, body)
	}
	return strings.Join(append(parts, after...), "\n\n"), nil
}

// substitute replaces each hook's matched span with its block, last span first
// so earlier offsets stay valid. Two hooks claiming overlapping spans is an
// error: neither can be applied without corrupting the other.
func substitute(prompt string, subs []firing) (string, error) {
	sort.SliceStable(subs, func(i, j int) bool { return subs[i].span[0] > subs[j].span[0] })
	for i, f := range subs {
		if i > 0 && f.span[1] > subs[i-1].span[0] {
			return "", fmt.Errorf("context hooks %s and %s substitute overlapping spans of the prompt", f.hook.Name, subs[i-1].hook.Name)
		}
		start, end := f.span[0], f.span[1]
		prompt = prompt[:start] + blocks.WrapReplacing(f.hook.Name, f.output, prompt[start:end]) + prompt[end:]
	}
	return prompt, nil
}

func recordApplied(metadata map[string]string, applied Applied) map[string]string {
	out := maps.Clone(metadata)
	if len(applied) == 0 {
		delete(out, MetadataKey)
		if len(out) == 0 {
			return nil
		}
		return out
	}
	if out == nil {
		out = map[string]string{}
	}
	out[MetadataKey] = strings.Join(applied.Names(), ",")
	return out
}
