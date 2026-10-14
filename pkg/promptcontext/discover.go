package promptcontext

import (
	"context"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	promptlib "github.com/flanksource/captain/pkg/ai/prompt"
	"github.com/flanksource/captain/pkg/ai/prompt/hookrun"
)

// Discover loads every prompt.context hook from sources, in order. A same-named
// prompt in a later source replaces an earlier one; `hook: {disabled: true}`
// removes it. Each hook's pattern and predicate are compiled here, so a broken
// hook fails discovery naming its source. Hooks are returned in name order.
func Discover(sources ...Source) ([]Hook, error) {
	byName := map[string]Hook{}
	for _, src := range sources {
		if src.FS == nil {
			return nil, fmt.Errorf("prompt context source %q has no FS", src.Label)
		}
		if err := load(src, byName); err != nil {
			return nil, err
		}
	}
	hooks := make([]Hook, 0, len(byName))
	for _, hook := range byName {
		hooks = append(hooks, hook)
	}
	sort.Slice(hooks, func(i, j int) bool { return hooks[i].Name < hooks[j].Name })
	return hooks, nil
}

func load(src Source, byName map[string]Hook) error {
	return fs.WalkDir(src.FS, ".", func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || path.Ext(file) != ".prompt" {
			return nil
		}
		tmpl, err := promptlib.LoadFS(src.FS, file)
		if err != nil {
			return err
		}
		source := src.Label + ":" + file
		spec, err := tmpl.Hook()
		if err != nil {
			return fmt.Errorf("%s: %w", source, err)
		}
		if spec == nil {
			return nil
		}
		name := strings.TrimSuffix(path.Base(file), ".prompt")
		if !promptlib.HookNameRe.MatchString(name) {
			return fmt.Errorf("%s: hook name %q must match %s", source, name, promptlib.HookNameRe)
		}
		if spec.Disabled {
			delete(byName, name)
			return nil
		}
		if spec.On != Event {
			return nil
		}
		hook, err := compile(tmpl, name, *spec)
		if err != nil {
			return fmt.Errorf("context hook %s (%s): %w", name, source, err)
		}
		hook.Source = source
		byName[name] = hook
		return nil
	})
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
