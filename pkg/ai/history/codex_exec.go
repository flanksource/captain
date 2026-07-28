package history

import (
	"fmt"
	"sort"
	"strings"

	"github.com/t14raptor/go-fast/ast"
	"github.com/t14raptor/go-fast/generator"
	"github.com/t14raptor/go-fast/parser"
	"mvdan.cc/sh/v3/syntax"
)

// Codex's freeform `exec` tool takes a JavaScript program, not a command: the
// model writes a script that calls tools.exec_command(), tools.apply_patch(),
// tools.write_stdin() and friends -- usually several times, driven off a literal
// table of commands -- and Codex runs it. A transcript should show the
// operations, not the driver, so the script is parsed and each invocation
// becomes a row.
const codexExecWrapperPrefix = "async function __captainCodexExec__() {\n"

// codexExecCommandTool is the one tool whose invocations merge: N shell commands
// from one script render as one shell block, exactly as if the model had written
// them on separate lines. Every other tool gets a row per invocation.
const codexExecCommandTool = "exec_command"

// codexScriptCall is a resolved tools.<name>(...) invocation. Args holds fully
// evaluated JavaScript values (string, float64, bool, []any, map[string]any) --
// an invocation whose arguments cannot be evaluated is an error, never a
// half-rendered guess with the source expression pasted in.
type codexScriptCall struct {
	Name     string
	Args     map[string]any
	Position ast.Idx
}

// parseCodexExecScript resolves every tools.* invocation in a freeform exec
// script, in source order. A script that has no tools.* call at all is not an
// error -- some scripts only print -- but a script that plainly calls one and
// yields nothing is a parser defect and says so.
func parseCodexExecScript(script string) ([]codexScriptCall, error) {
	program, err := parser.Parse(codexExecWrapperPrefix + script + "\n}")
	if err != nil {
		return nil, fmt.Errorf("parse Codex exec JavaScript: %w", err)
	}

	visitor := &codexExecVisitor{scope: newCodexScriptScope(nil)}
	visitor.V = visitor
	program.VisitWith(visitor)
	if visitor.err != nil {
		return nil, visitor.err
	}
	if len(visitor.calls) == 0 {
		if sites := countCodexToolCallSites(program); sites > 0 {
			// Returning nil here instead is what hid a 0.17% parse rate for the
			// whole life of the feature: the caller cannot distinguish "no tools
			// were called" from "the traversal missed them".
			return nil, fmt.Errorf(
				"parse Codex exec JavaScript: %d tools.* call site(s) resolved to no invocation", sites)
		}
		return nil, nil
	}
	sort.SliceStable(visitor.calls, func(i, j int) bool {
		return visitor.calls[i].Position < visitor.calls[j].Position
	})
	return visitor.calls, nil
}

type codexExecVisitor struct {
	ast.NoopVisitor
	scope *codexScriptScope
	calls []codexScriptCall
	err   error
}

// countCodexToolCallSites counts every tools.<name>(...) call site in the whole
// program, descending unconditionally rather than following the resolver's
// scope-aware traversal. It is the honest denominator for "the resolver produced
// nothing": a mention of tools.exec_command inside a string literal or a comment
// is not a call site, so it cannot make a clean parse look like a defect, while a
// call the resolver's traversal never reached still does.
func countCodexToolCallSites(program *ast.Program) int {
	counter := &codexToolCallSiteCounter{}
	counter.V = counter
	program.VisitWith(counter)
	return counter.count
}

type codexToolCallSiteCounter struct {
	ast.NoopVisitor
	count int
}

func (c *codexToolCallSiteCounter) VisitCallExpression(call *ast.CallExpression) {
	if _, ok := codexToolCallee(call.Callee); ok {
		c.count++
	}
	call.VisitChildrenWith(c)
}

func (v *codexExecVisitor) fail(err error) {
	if v.err == nil {
		v.err = err
	}
}

func (v *codexExecVisitor) VisitVariableDeclaration(declaration *ast.VariableDeclaration) {
	for index := range declaration.List {
		declarator := &declaration.List[index]
		if declarator.Initializer == nil {
			continue
		}
		if value, ok := v.eval(declarator.Initializer); ok {
			v.scope.bind(declarator.Target, value)
		}
	}
	declaration.VisitChildrenWith(v)
}

func (v *codexExecVisitor) VisitCallExpression(call *ast.CallExpression) {
	if v.err != nil {
		return
	}
	if name, ok := codexToolCallee(call.Callee); ok {
		resolved, err := v.resolveScriptCall(name, call)
		if err != nil {
			v.fail(err)
			return
		}
		v.calls = append(v.calls, resolved)
		return
	}
	if v.unrollIteration(call) {
		return
	}
	call.VisitChildrenWith(v)
}

func (v *codexExecVisitor) VisitForOfStatement(statement *ast.ForOfStatement) {
	if v.err != nil {
		return
	}
	elements, ok := v.evalArray(statement.Source)
	if !ok {
		statement.VisitChildrenWith(v)
		return
	}
	target := codexForOfTarget(statement.Into)
	for _, element := range elements {
		v.inChildScope(func(scope *codexScriptScope) {
			if target != nil {
				scope.bind(target, element)
			}
			statement.Body.VisitWith(v)
		})
		if v.err != nil {
			return
		}
	}
}

// unrollIteration expands `<array>.map(fn)` / `.forEach(fn)` / `.flatMap(fn)`
// over a resolvable array by visiting the callback once per element with its
// parameters bound. A literal table of commands pushed through Promise.all is
// how the model writes almost every multi-command script, and without this the
// tool calls inside the callback are never reached.
func (v *codexExecVisitor) unrollIteration(call *ast.CallExpression) bool {
	member, ok := call.Callee.Member()
	if !ok || len(call.ArgumentList) == 0 {
		return false
	}
	property, ok := member.Property.Identifier()
	if !ok {
		return false
	}
	switch property.Name {
	case "map", "forEach", "flatMap":
	default:
		return false
	}
	parameters, body, ok := codexCallbackParts(&call.ArgumentList[0])
	if !ok {
		return false
	}
	elements, ok := v.evalArray(member.Object)
	if !ok {
		return false
	}
	for index, element := range elements {
		v.inChildScope(func(scope *codexScriptScope) {
			bindCodexCallbackParameters(scope, parameters, element, index)
			body(v)
		})
		if v.err != nil {
			return true
		}
	}
	return true
}

func (v *codexExecVisitor) inChildScope(run func(scope *codexScriptScope)) {
	parent := v.scope
	v.scope = newCodexScriptScope(parent)
	run(v.scope)
	v.scope = parent
}

func (v *codexExecVisitor) resolveScriptCall(name string, call *ast.CallExpression) (codexScriptCall, error) {
	if len(call.ArgumentList) > 1 {
		return codexScriptCall{}, fmt.Errorf(
			"parse Codex exec JavaScript: tools.%s at offset %d takes one argument, got %d",
			name, call.Idx0(), len(call.ArgumentList))
	}
	resolved := codexScriptCall{Name: name, Args: map[string]any{}, Position: call.Idx0()}
	if len(call.ArgumentList) == 0 {
		return resolved, nil
	}
	argument := &call.ArgumentList[0]
	value, ok := v.eval(argument)
	if !ok {
		return codexScriptCall{}, fmt.Errorf(
			"parse Codex exec JavaScript: tools.%s at offset %d has an unresolvable argument %s",
			name, call.Idx0(), generator.GenerateMinified(argument))
	}
	if fields, ok := value.(map[string]any); ok {
		resolved.Args = fields
		return resolved, nil
	}
	// Positional form, e.g. tools.apply_patch("*** Begin Patch\n..."). "input"
	// is the key the non-freeform custom_tool_call form uses for the same
	// payload, so both shapes reach the same normalization.
	resolved.Args["input"] = value
	return resolved, nil
}

// codexToolCallee returns the tool name for a `tools.<name>` callee, joining
// namespaces with a dot so `tools.mcp.search` reads as "mcp.search".
func codexToolCallee(expression *ast.Expression) (string, bool) {
	member, ok := expression.Member()
	if !ok {
		return "", false
	}
	property, ok := member.Property.Identifier()
	if !ok {
		return "", false
	}
	if object, ok := member.Object.Identifier(); ok {
		if object.Name != "tools" {
			return "", false
		}
		return property.Name, true
	}
	prefix, ok := codexToolCallee(member.Object)
	if !ok {
		return "", false
	}
	return prefix + "." + property.Name, true
}

// codexCallbackParts splits an iteration callback into its parameters and a
// function that visits its body, covering both `x => …` and `function (x) {…}`.
func codexCallbackParts(expression *ast.Expression) (*ast.ParameterList, func(ast.Visitor), bool) {
	if arrow, ok := expression.ArrowFuncLit(); ok {
		return arrow.ParameterList, func(v ast.Visitor) { arrow.Body.VisitWith(v) }, true
	}
	if function, ok := expression.FuncLit(); ok {
		return function.ParameterList, func(v ast.Visitor) { function.Body.VisitWith(v) }, true
	}
	return nil, nil, false
}

func bindCodexCallbackParameters(scope *codexScriptScope, parameters *ast.ParameterList, element any, index int) {
	if parameters == nil || len(parameters.List) == 0 {
		return
	}
	scope.bind(parameters.List[0].Target, element)
	if len(parameters.List) > 1 {
		scope.bind(parameters.List[1].Target, float64(index))
	}
}

func codexForOfTarget(into *ast.ForInto) *ast.Pattern {
	if into == nil {
		return nil
	}
	if pattern, ok := into.Pattern(); ok {
		return pattern
	}
	if declaration, ok := into.VarDecl(); ok && len(declaration.List) > 0 {
		return declaration.List[0].Target
	}
	return nil
}

// codexExecCommand is one resolved tools.exec_command() invocation, ready to
// render as a shell line.
type codexExecCommand struct {
	command    string
	workdir    string
	hasWorkdir bool
}

func codexExecCommandFrom(call codexScriptCall) (codexExecCommand, error) {
	command, ok := codexScriptCommand(call.Args["cmd"])
	if !ok {
		return codexExecCommand{}, fmt.Errorf(
			"parse Codex exec JavaScript: tools.exec_command at offset %d is missing cmd", call.Position)
	}
	resolved := codexExecCommand{command: command}
	raw, ok := call.Args["workdir"]
	if !ok || raw == nil {
		return resolved, nil
	}
	workdir, ok := codexScriptString(raw)
	if !ok {
		return codexExecCommand{}, fmt.Errorf(
			"parse Codex exec JavaScript: tools.exec_command at offset %d has a non-string workdir", call.Position)
	}
	resolved.workdir, resolved.hasWorkdir = workdir, true
	return resolved, nil
}

// codexScriptCommand renders the cmd argument. Codex accepts either a shell
// string or an argv array; the array form is quoted back into a shell line so
// both render identically.
func codexScriptCommand(value any) (string, bool) {
	if text, ok := value.(string); ok {
		return text, text != ""
	}
	items, ok := value.([]any)
	if !ok || len(items) == 0 {
		return "", false
	}
	parts := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := codexScriptString(item)
		if !ok {
			return "", false
		}
		quoted, err := syntax.Quote(text, syntax.LangBash)
		if err != nil {
			return "", false
		}
		parts = append(parts, quoted)
	}
	return strings.Join(parts, " "), true
}

// renderCodexExecCommands folds a script's shell commands into one block. When
// every command shares a working directory the block is just the commands and
// the directory is reported separately; otherwise each command carries its own
// `cd` subshell so the rendered text stays runnable.
func renderCodexExecCommands(commands []codexExecCommand, cwd string) (string, string, error) {
	commonWorkdir, common := codexExecCommonWorkdir(commands, cwd)
	rendered := make([]string, 0, len(commands))
	for _, command := range commands {
		if common {
			rendered = append(rendered, command.command)
			continue
		}
		workdir := cwd
		if command.hasWorkdir {
			workdir = command.workdir
		}
		block, err := renderCodexExecCommand(command.command, workdir)
		if err != nil {
			return "", cwd, err
		}
		rendered = append(rendered, block)
	}
	if !common {
		commonWorkdir = cwd
	}
	return strings.Join(rendered, "\n"), commonWorkdir, nil
}

func codexExecCommonWorkdir(commands []codexExecCommand, cwd string) (string, bool) {
	common := ""
	for index, command := range commands {
		workdir := cwd
		if command.hasWorkdir {
			workdir = command.workdir
		}
		if index == 0 {
			common = workdir
			continue
		}
		if workdir != common {
			return "", false
		}
	}
	return common, true
}

func renderCodexExecCommand(command, workdir string) (string, error) {
	if workdir == "" {
		return command, nil
	}
	quotedWorkdir, err := syntax.Quote(workdir, syntax.LangBash)
	if err != nil {
		return "", fmt.Errorf("quote Codex exec workdir: %w", err)
	}
	return strings.Join([]string{
		"(",
		"  cd " + quotedWorkdir,
		"  " + strings.ReplaceAll(command, "\n", "\n  "),
		")",
	}, "\n"), nil
}
