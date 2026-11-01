// Package hookaction runs the tool and prompt actions shared by schema hooks
// (output.hooks) and lifecycle hooks (workflow.after): it renders an action's
// `with` against the hook's variables and dispatches it to a Runner.
package hookaction

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"

	"github.com/mbleigh/raymond"

	"github.com/flanksource/captain/pkg/api"
)

// Runner executes an action against the run it belongs to: an inline tool by
// name with input as its stdin object, or a child .prompt with vars as its
// variables, run in cwd (empty is the parent run's). Each returns the action's
// output — the tool's decoded result, the child's structured output (else its
// text).
type Runner interface {
	RunTool(ctx context.Context, name string, input map[string]any) (any, error)
	RunPrompt(ctx context.Context, path string, vars map[string]any, cwd string) (any, error)
}

// Call is one dispatch: the action, the variables its `with` renders over, the
// input its rendered `with` is merged over, and where a child prompt runs.
type Call struct {
	Action api.HookAction
	Vars   map[string]any
	Base   map[string]any
	Cwd    string
}

var lonePath = regexp.MustCompile(`^\{\{\s*([A-Za-z_][\w-]*(?:\.[\w-]+)*)\s*\}\}$`)

// Run renders the action's `with` over call.Vars, merges it over call.Base, and
// dispatches the action. Commands are not dispatched here: they run where the
// caller runs shell.
func Run(ctx context.Context, call Call, runner Runner) (any, error) {
	with, err := RenderWith(call.Action.With, call.Vars)
	if err != nil {
		return nil, err
	}
	input := maps.Clone(call.Base)
	if input == nil {
		input = map[string]any{}
	}
	maps.Copy(input, with)
	switch {
	case call.Action.Tool != "":
		return runner.RunTool(ctx, call.Action.Tool, input)
	case call.Action.Prompt != "":
		return runner.RunPrompt(ctx, call.Action.Prompt, input, call.Cwd)
	}
	return nil, fmt.Errorf("hookaction: only tool and prompt actions are dispatched here")
}

// RenderWith renders each value of with: a string that is exactly one {{path}}
// binds the native value at that path (a missing path is an error), any other
// string renders as Handlebars to a string, and maps and lists recurse.
func RenderWith(with map[string]any, vars map[string]any) (map[string]any, error) {
	if len(with) == 0 {
		return map[string]any{}, nil
	}
	native, err := Native(vars)
	if err != nil {
		return nil, fmt.Errorf("hook variables: %w", err)
	}
	data, _ := native.(map[string]any)
	out := make(map[string]any, len(with))
	for key, value := range with {
		rendered, err := renderValue(value, data)
		if err != nil {
			return nil, fmt.Errorf("with.%s: %w", key, err)
		}
		out[key] = rendered
	}
	return out, nil
}

func renderValue(value any, vars map[string]any) (any, error) {
	switch v := value.(type) {
	case string:
		return renderString(v, vars)
	case map[string]any:
		return RenderWith(v, vars)
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			rendered, err := renderValue(item, vars)
			if err != nil {
				return nil, fmt.Errorf("[%d]: %w", i, err)
			}
			out[i] = rendered
		}
		return out, nil
	}
	return value, nil
}

func renderString(value string, vars map[string]any) (any, error) {
	if match := lonePath.FindStringSubmatch(strings.TrimSpace(value)); match != nil {
		resolved, err := Lookup(vars, match[1])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", value, err)
		}
		return resolved, nil
	}
	if !strings.Contains(value, "{{") {
		return value, nil
	}
	tpl, err := raymond.Parse(value)
	if err != nil {
		return nil, fmt.Errorf("parse %q: %w", value, err)
	}
	tpl.RegisterHelper("json", jsonHelper)
	out, err := tpl.ExecWith(vars, raymond.NewDataFrame(), &raymond.ExecOptions{NoEscape: true})
	if err != nil {
		return nil, fmt.Errorf("render %q: %w", value, err)
	}
	return out, nil
}

func jsonHelper(value any) raymond.SafeString {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("json helper: %v", err))
	}
	return raymond.SafeString(raw)
}

// Lookup walks a dotted path through maps and lists (a numeric segment indexes
// a list), failing on the first segment that names nothing.
func Lookup(vars map[string]any, path string) (any, error) {
	var current any = vars
	for _, segment := range strings.Split(path, ".") {
		switch node := current.(type) {
		case map[string]any:
			value, ok := node[segment]
			if !ok {
				return nil, fmt.Errorf("%q not found", segment)
			}
			current = value
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil, fmt.Errorf("%q is not an index of a %d-item list", segment, len(node))
			}
			current = node[index]
		default:
			return nil, fmt.Errorf("%q not found: %T has no fields", segment, current)
		}
	}
	return current, nil
}

// Native round-trips a value through JSON so typed Go values (a struct, a
// map[string]string) become the maps, lists and scalars CEL and templates read.
func Native(value any) (any, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
