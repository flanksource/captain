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

const codexExecWrapperPrefix = "async function __captainCodexExec__() {\n"

type codexExecCommand struct {
	command        string
	workdir        string
	workdirDynamic bool
	hasWorkdir     bool
	position       ast.Idx
}

type codexExecVisitor struct {
	ast.NoopVisitor
	commands []codexExecCommand
	err      error
}

func parseCodexExecScript(script, cwd string) (string, string, error) {
	source := codexExecWrapperPrefix + script + "\n}"
	program, err := parser.Parse(source)
	if err != nil {
		return "", cwd, fmt.Errorf("parse Codex exec JavaScript: %w", err)
	}

	visitor := &codexExecVisitor{}
	visitor.V = visitor
	program.VisitWith(visitor)
	if visitor.err != nil {
		return "", cwd, visitor.err
	}
	if len(visitor.commands) == 0 {
		return "", cwd, nil
	}

	sort.SliceStable(visitor.commands, func(i, j int) bool {
		return visitor.commands[i].position < visitor.commands[j].position
	})
	return renderCodexExecCommands(visitor.commands, cwd)
}

func (v *codexExecVisitor) VisitCallExpression(call *ast.CallExpression) {
	if !isCodexExecCommandCallee(call.Callee) {
		call.VisitChildrenWith(v)
		return
	}
	if v.err != nil {
		return
	}
	command, err := v.parseCommand(call)
	if err != nil {
		v.err = err
		return
	}
	v.commands = append(v.commands, command)
}

func isCodexExecCommandCallee(expression *ast.Expression) bool {
	member, ok := expression.Member()
	if !ok {
		return false
	}
	object, ok := member.Object.Identifier()
	if !ok || object.Name != "tools" {
		return false
	}
	property, ok := member.Property.Identifier()
	return ok && property.Name == "exec_command"
}

func (v *codexExecVisitor) parseCommand(call *ast.CallExpression) (codexExecCommand, error) {
	if len(call.ArgumentList) != 1 {
		return codexExecCommand{}, fmt.Errorf(
			"parse Codex exec JavaScript: tools.exec_command at offset %d requires one argument",
			call.Idx0(),
		)
	}

	argument := &call.ArgumentList[0]
	command := codexExecCommand{position: call.Idx0()}
	object, ok := argument.ObjectLit()
	if !ok {
		source := generator.GenerateMinified(argument)
		command.command = codexJSPlaceholder(source + ".cmd")
		command.workdir = codexJSPlaceholder(source + ".workdir")
		command.workdirDynamic = true
		command.hasWorkdir = true
		return command, nil
	}

	hasSpread := v.parseCommandObject(object, &command)
	if hasSpread {
		source := generator.GenerateMinified(argument)
		if command.command == "" {
			command.command = codexJSPlaceholder(source + ".cmd")
		}
		if !command.hasWorkdir {
			command.workdir = codexJSPlaceholder(source + ".workdir")
			command.workdirDynamic = true
			command.hasWorkdir = true
		}
	}
	if command.command == "" {
		return codexExecCommand{}, fmt.Errorf(
			"parse Codex exec JavaScript: tools.exec_command at offset %d is missing cmd",
			call.Idx0(),
		)
	}
	return command, nil
}

func (v *codexExecVisitor) parseCommandObject(object *ast.ObjectLiteral, command *codexExecCommand) bool {
	hasSpread := false
	for index := range object.Value {
		property := &object.Value[index]
		if property.IsSpread() {
			hasSpread = true
			continue
		}
		if keyValue, ok := property.KeyValue(); ok {
			key, ok := keyValue.Key.StringLit()
			if !ok {
				continue
			}
			switch key.Value {
			case "cmd":
				command.command, _ = v.renderValue(keyValue.Value)
			case "workdir":
				command.workdir, command.workdirDynamic = v.renderValue(keyValue.Value)
				command.hasWorkdir = true
			}
			continue
		}
		if shorthand, ok := property.Short(); ok {
			switch shorthand.Name.Name {
			case "cmd":
				command.command = codexJSPlaceholder("cmd")
			case "workdir":
				command.workdir = codexJSPlaceholder("workdir")
				command.workdirDynamic = true
				command.hasWorkdir = true
			}
		}
	}
	return hasSpread
}

func (v *codexExecVisitor) renderValue(expression *ast.Expression) (string, bool) {
	if literal, ok := expression.StringLit(); ok {
		return literal.Value, false
	}
	if template, ok := expression.TmplLit(); ok {
		var value strings.Builder
		for index := range template.Elements {
			value.WriteString(template.Elements[index].Parsed)
			if index < len(template.Expressions) {
				value.WriteString(codexJSPlaceholder(generator.GenerateMinified(&template.Expressions[index])))
			}
		}
		return value.String(), len(template.Expressions) > 0
	}
	return codexJSPlaceholder(generator.GenerateMinified(expression)), true
}

func codexJSPlaceholder(expression string) string {
	expression = strings.TrimSpace(expression)
	if expression == "" {
		expression = "unknown"
	}
	return "{{js:" + expression + "}}"
}

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
		block, err := renderCodexExecCommand(command.command, workdir, command.workdirDynamic)
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
		if command.workdirDynamic {
			return "", false
		}
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

func renderCodexExecCommand(command, workdir string, workdirDynamic bool) (string, error) {
	if workdir == "" {
		return command, nil
	}
	quotedWorkdir := workdir
	if !workdirDynamic {
		var err error
		quotedWorkdir, err = syntax.Quote(workdir, syntax.LangBash)
		if err != nil {
			return "", fmt.Errorf("quote Codex exec workdir: %w", err)
		}
	}
	return strings.Join([]string{
		"(",
		"  cd " + quotedWorkdir,
		"  " + strings.ReplaceAll(command, "\n", "\n  "),
		")",
	}, "\n"), nil
}
