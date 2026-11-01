package api

import (
	"fmt"
	"strings"

	"github.com/google/cel-go/cel"
)

// HookAction is one thing a hook does: run an inline tool, run a child .prompt,
// or run shell commands. Exactly one of Tool, Prompt or Commands is set. If is a
// CEL predicate over the hook's variables; empty always runs. With is the
// tool's stdin object or the child prompt's variables: a string that is exactly
// one {{path}} expression binds the native value at that path, any other string
// is rendered as Handlebars, and other values are rendered recursively.
type HookAction struct {
	If       string         `json:"if,omitempty" yaml:"if,omitempty"`
	Tool     string         `json:"tool,omitempty" yaml:"tool,omitempty"`
	Prompt   string         `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	With     map[string]any `json:"with,omitempty" yaml:"with,omitempty"`
	Commands []string       `json:"commands,omitempty" yaml:"commands,omitempty"`
}

// HookActionKind names what a HookAction runs.
type HookActionKind string

const (
	HookActionTool     HookActionKind = "tool"
	HookActionPrompt   HookActionKind = "prompt"
	HookActionCommands HookActionKind = "commands"
)

// HookEnv names the variable set a hook's `if` and `with` are evaluated over.
type HookEnv string

const (
	// HookEnvSchema is an output.hooks schema hook: vars, metadata, labels, model.
	HookEnvSchema HookEnv = "schema"
	// HookEnvInput is an input.hooks input hook: vars, metadata, labels, model.
	HookEnvInput HookEnv = "input"
	// HookEnvLifecycle is a workflow.after step: output, result, vars, metadata,
	// outcome, steps.
	HookEnvLifecycle HookEnv = "lifecycle"
)

// Kind returns the one action set, failing when none or several are.
func (a HookAction) Kind() (HookActionKind, error) {
	var kinds []HookActionKind
	if strings.TrimSpace(a.Tool) != "" {
		kinds = append(kinds, HookActionTool)
	}
	if strings.TrimSpace(a.Prompt) != "" {
		kinds = append(kinds, HookActionPrompt)
	}
	if len(a.Commands) > 0 {
		kinds = append(kinds, HookActionCommands)
	}
	switch len(kinds) {
	case 0:
		return "", fmt.Errorf("declares no action: set one of tool, prompt or commands")
	case 1:
		return kinds[0], nil
	}
	return "", fmt.Errorf("declares %d actions (%v): set exactly one of tool, prompt or commands", len(kinds), kinds)
}

// Validate checks the action's shape and compiles its `if` in env.
func (a HookAction) Validate(env HookEnv) error {
	kind, err := a.Kind()
	if err != nil {
		return err
	}
	if kind == HookActionCommands {
		if len(a.With) > 0 {
			return fmt.Errorf("with is only read by a tool or prompt action, not by commands")
		}
		for i, cmd := range a.Commands {
			if strings.TrimSpace(cmd) == "" {
				return fmt.Errorf("commands[%d] is empty", i)
			}
		}
	}
	if strings.TrimSpace(a.If) != "" {
		if _, err := CompileHookIf(env, a.If); err != nil {
			return err
		}
	}
	return nil
}

// HookVariables are the names each hook env declares, in documentation order.
func HookVariables(env HookEnv) []string {
	if env == HookEnvSchema || env == HookEnvInput {
		return []string{"vars", "metadata", "labels", "model"}
	}
	return []string{"output", "result", "vars", "metadata", "outcome", "steps"}
}

func hookCELEnv(env HookEnv) (*cel.Env, error) {
	dynMap := cel.MapType(cel.StringType, cel.DynType)
	stringMap := cel.MapType(cel.StringType, cel.StringType)
	switch env {
	case HookEnvSchema, HookEnvInput:
		return cel.NewEnv(
			cel.Variable("vars", dynMap),
			cel.Variable("metadata", stringMap),
			cel.Variable("labels", stringMap),
			cel.Variable("model", cel.StringType),
		)
	case HookEnvLifecycle:
		return cel.NewEnv(
			cel.Variable("output", cel.DynType),
			cel.Variable("result", cel.StringType),
			cel.Variable("vars", dynMap),
			cel.Variable("metadata", stringMap),
			cel.Variable("outcome", cel.MapType(cel.StringType, cel.BoolType)),
			cel.Variable("steps", cel.ListType(cel.DynType)),
		)
	}
	return nil, fmt.Errorf("unknown hook env %q", env)
}

// CompileHookIf compiles a hook's `if` predicate in a strict env: a variable the
// env does not declare is a compile error, so a typo fails at load time instead
// of never matching.
func CompileHookIf(env HookEnv, expr string) (cel.Program, error) {
	celEnv, err := hookCELEnv(env)
	if err != nil {
		return nil, err
	}
	ast, issues := celEnv.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("if %q: %w", expr, issues.Err())
	}
	if out := ast.OutputType(); !out.IsExactType(cel.BoolType) && !out.IsExactType(cel.DynType) {
		return nil, fmt.Errorf("if %q must return bool, got %s", expr, out)
	}
	program, err := celEnv.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("if %q: %w", expr, err)
	}
	return program, nil
}

// EvalHookIf evaluates a's `if` over vars; an empty predicate is true.
func (a HookAction) EvalIf(env HookEnv, vars map[string]any) (bool, error) {
	if strings.TrimSpace(a.If) == "" {
		return true, nil
	}
	program, err := CompileHookIf(env, a.If)
	if err != nil {
		return false, err
	}
	out, _, err := program.Eval(vars)
	if err != nil {
		return false, fmt.Errorf("if %q: %w", a.If, err)
	}
	value, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("if %q did not return a bool: got %T(%v)", a.If, out.Value(), out.Value())
	}
	return value, nil
}
