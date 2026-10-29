package promptcontext

import (
	"context"
	"fmt"
	"regexp"
	"slices"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/ai/prompt/hookrun"
)

// Discover loads every prompt.context hook from sources, in order. A same-named
// prompt in a later source replaces an earlier one; `hook: {disabled: true}`
// removes it. Each hook's pattern and predicate are compiled here, so a broken
// hook fails discovery naming its source. Hooks are returned in name order.
func Discover(sources ...Source) ([]Hook, error) {
	files, err := promptlib.DiscoverHookFiles(Event, sources...)
	if err != nil {
		return nil, err
	}
	hooks := make([]Hook, 0, len(files))
	for _, file := range files {
		hook, err := compile(file.Template, file.Name, file.Spec)
		if err != nil {
			return nil, fmt.Errorf("context hook %s (%s): %w", file.Name, file.Source, err)
		}
		hook.Source = file.Source
		hooks = append(hooks, hook)
	}
	return hooks, nil
}

// compile validates spec for prompt.context and builds its matcher, predicate
// and renderer.
func compile(tmpl *promptlib.Template, name string, spec promptlib.HookSpec) (Hook, error) {
	if err := validate(spec); err != nil {
		return Hook{}, err
	}
	hook := Hook{Name: name, Mode: spec.Mode, render: renderer(tmpl, name, spec)}
	if spec.Match != "" {
		match, err := compileMatch(spec.Match)
		if err != nil {
			return Hook{}, err
		}
		hook.match = match
	}
	if spec.When != "" {
		program, err := compileWhen(spec.When)
		if err != nil {
			return Hook{}, err
		}
		hook.when, hook.expr = program, spec.When
	}
	return hook, nil
}

// validate rejects what prompt.context has no meaning for. An unconditional hook
// would enrich every prompt of every run; whole-prompt replace would discard the
// authored request and could not be undone on re-apply — substitute covers a
// targeted rewrite.
func validate(spec promptlib.HookSpec) error {
	switch {
	case spec.Match == "" && spec.When == "":
		return fmt.Errorf("%s hooks require `match` or `when`", Event)
	case spec.Mode == promptlib.HookReplace:
		return fmt.Errorf("%s hooks do not support mode %s; use substitute with match", Event, spec.Mode)
	case spec.Field != "" && spec.Render != promptlib.HookRenderModel:
		return fmt.Errorf("field requires render: model")
	}
	return nil
}

func compileMatch(pattern string) (*regexp.Regexp, error) {
	match, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("match: %w", err)
	}
	for _, group := range match.SubexpNames() {
		if slices.Contains(variableNames, group) {
			return nil, fmt.Errorf("match: capture %q shadows the %s variable of the same name", group, Event)
		}
	}
	return match, nil
}

func renderer(tmpl *promptlib.Template, name string, spec promptlib.HookSpec) func(context.Context, map[string]any) (string, error) {
	if spec.Render == promptlib.HookRenderModel {
		return func(ctx context.Context, data map[string]any) (string, error) {
			return hookrun.Execute(ctx, tmpl, hookrun.Options{Name: "context hook " + name, Field: spec.Field, Data: data})
		}
	}
	return func(_ context.Context, data map[string]any) (string, error) {
		req, _, err := tmpl.Render(promptlib.RenderOptions{Data: data, Declared: true})
		if err != nil {
			return "", err
		}
		if req.Prompt.System != "" {
			return "", fmt.Errorf("a template-rendered context hook renders user text only, got a system message")
		}
		return req.Prompt.User, nil
	}
}
