package promptcontext

import (
	"fmt"
	"sync"

	"github.com/google/cel-go/cel"
)

// The variables a hook's `when` predicate and body can read.
const (
	VarPrompt   = "prompt"
	VarSource   = "source"
	VarMetadata = "metadata"
	VarLabels   = "labels"
	VarModel    = "model"
	VarCwd      = "cwd"
	VarCaptures = "captures"
)

var variableNames = []string{VarPrompt, VarSource, VarMetadata, VarLabels, VarModel, VarCwd, VarCaptures}

// whenEnv is strict on purpose: a name no declaration covers is a compile
// error, so a typo fails discovery instead of quietly never matching.
var whenEnv = sync.OnceValues(func() (*cel.Env, error) {
	stringMap := cel.MapType(cel.StringType, cel.StringType)
	return cel.NewEnv(
		cel.Variable(VarPrompt, cel.StringType),
		cel.Variable(VarSource, cel.StringType),
		cel.Variable(VarMetadata, stringMap),
		cel.Variable(VarLabels, stringMap),
		cel.Variable(VarModel, cel.StringType),
		cel.Variable(VarCwd, cel.StringType),
		cel.Variable(VarCaptures, stringMap),
	)
})

func compileWhen(expr string) (cel.Program, error) {
	env, err := whenEnv()
	if err != nil {
		return nil, fmt.Errorf("when env: %w", err)
	}
	ast, issues := env.Compile(expr)
	if issues != nil && issues.Err() != nil {
		return nil, fmt.Errorf("when %q: %w", expr, issues.Err())
	}
	if out := ast.OutputType(); !out.IsExactType(cel.BoolType) && !out.IsExactType(cel.DynType) {
		return nil, fmt.Errorf("when %q must return bool, got %s", expr, out)
	}
	program, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("when %q: %w", expr, err)
	}
	return program, nil
}

// evalWhen evaluates a predicate; anything but a bool result is an error.
func evalWhen(program cel.Program, expr string, vars map[string]any) (bool, error) {
	out, _, err := program.Eval(vars)
	if err != nil {
		return false, fmt.Errorf("when %q: %w", expr, err)
	}
	value, ok := out.Value().(bool)
	if !ok {
		return false, fmt.Errorf("when %q did not return a bool: got %T(%v)", expr, out.Value(), out.Value())
	}
	return value, nil
}
