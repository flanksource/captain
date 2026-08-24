package bash

import (
	"maps"
	"path/filepath"
	"strings"
	"sync"

	"mvdan.cc/sh/v3/syntax"
)

// shellParsers reuses the shell parser across calls. A parser holds a sizeable
// scratch buffer and is not safe for concurrent use, but it is reusable in
// sequence -- and building a fresh one per command was the largest single
// allocation in normalizing a transcript's Bash calls.
//
// Returning the parser before the caller is done with the AST is safe because
// Parser.reset drops its node batches on every Parse: each call allocates the
// nodes it returns, so a later Parse cannot write over an earlier File.
var shellParsers = sync.Pool{New: func() any { return syntax.NewParser() }}

type ShellCommand struct {
	Command string
	Shell   string
	Flags   []string
	Args    []string
}

func TransformShellCommand(command string) (ShellCommand, bool) {
	parser := shellParsers.Get().(*syntax.Parser)
	file, err := parser.Parse(strings.NewReader(command), "")
	shellParsers.Put(parser)
	if err != nil || len(file.Stmts) != 1 || len(file.Stmts[0].Redirs) > 0 {
		return ShellCommand{}, false
	}
	call, ok := file.Stmts[0].Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) > 0 || len(call.Args) < 3 {
		return ShellCommand{}, false
	}
	args, ok := staticWords(call.Args)
	if !ok {
		return ShellCommand{}, false
	}
	shell := filepath.Base(args[0])
	if shell != "sh" && shell != "bash" && shell != "zsh" {
		return ShellCommand{}, false
	}

	flags, commandIndex, ok := shellCommandFlag(args[1:])
	if !ok || commandIndex+2 >= len(args) {
		return ShellCommand{}, false
	}
	return ShellCommand{
		Command: args[commandIndex+2],
		Shell:   shell,
		Flags:   flags,
		Args:    append([]string(nil), args[commandIndex+3:]...),
	}, true
}

func TransformBashInput(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	transformed := maps.Clone(input)
	if shell, _ := transformed["shell"].(string); shell != "" {
		return transformed
	}
	command, _ := transformed["command"].(string)
	wrapped, ok := TransformShellCommand(command)
	if !ok {
		return transformed
	}
	transformed["command"] = wrapped.Command
	transformed["shell"] = wrapped.Shell
	if len(wrapped.Flags) > 0 {
		transformed["shellFlags"] = wrapped.Flags
	}
	if len(wrapped.Args) > 0 {
		transformed["shellArgs"] = wrapped.Args
	}
	return transformed
}

func staticWords(words []*syntax.Word) ([]string, bool) {
	values := make([]string, len(words))
	for i, word := range words {
		if !isStaticWord(word) {
			return nil, false
		}
		values[i] = wordToString(word)
	}
	return values, true
}

func isStaticWord(word *syntax.Word) bool {
	if word == nil {
		return false
	}
	for _, part := range word.Parts {
		switch value := part.(type) {
		case *syntax.Lit, *syntax.SglQuoted:
		case *syntax.DblQuoted:
			if !isStaticWord(&syntax.Word{Parts: value.Parts}) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func shellCommandFlag(args []string) ([]string, int, bool) {
	var flags []string
	for i, arg := range args {
		if arg == "--" || !strings.HasPrefix(arg, "-") || arg == "-" {
			return nil, 0, false
		}
		if strings.HasPrefix(arg, "--") {
			flags = append(flags, arg)
			continue
		}
		options := strings.TrimPrefix(arg, "-")
		commandOption := strings.IndexByte(options, 'c')
		if commandOption < 0 {
			flags = append(flags, arg)
			continue
		}
		if commandOption != len(options)-1 {
			return nil, 0, false
		}
		if remaining := options[:commandOption]; remaining != "" {
			flags = append(flags, "-"+remaining)
		}
		return flags, i, true
	}
	return nil, 0, false
}
