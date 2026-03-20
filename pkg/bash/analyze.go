package bash

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

func Analyze(script string) (*AnalysisResult, error) {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(script), "")
	if err != nil {
		return analyzeFirstLine(script), err
	}

	result := &AnalysisResult{}
	syntax.Walk(file, func(node syntax.Node) bool {
		if stmt, ok := node.(*syntax.Stmt); ok {
			handleRedirects(stmt, &result.Operations)
		}
		if call, ok := node.(*syntax.CallExpr); ok {
			handleCall(call, &result.Operations)
			extractCommandAndPaths(call, result)
		}
		return true
	})

	return result, nil
}

func analyzeFirstLine(script string) *AnalysisResult {
	result := &AnalysisResult{}
	line := strings.TrimSpace(strings.SplitN(script, "\n", 2)[0])
	if line == "" {
		return result
	}
	if idx := strings.Index(line, "<<"); idx > 0 {
		line = strings.TrimSpace(line[:idx])
	}
	if fields := strings.Fields(line); len(fields) > 0 {
		result.Commands = append(result.Commands, strings.Join(fields, " "))
	}
	return result
}

var inlineInterpreters = map[string]bool{
	"python": true, "python3": true, "python2": true,
	"node": true, "ruby": true, "perl": true, "php": true,
	"bash": true, "sh": true, "zsh": true,
}

func extractCommandAndPaths(call *syntax.CallExpr, result *AnalysisResult) {
	if len(call.Args) == 0 {
		return
	}

	var parts []string
	for _, arg := range call.Args {
		parts = append(parts, wordToString(arg))
	}
	result.Commands = append(result.Commands, strings.Join(parts, " "))

	cmdName := parts[0]
	if inlineInterpreters[cmdName] {
		return
	}

	if cmdName == "cd" && len(parts) > 1 {
		result.ReferencedPaths = append(result.ReferencedPaths, parts[1])
		return
	}

	for i := 1; i < len(parts); i++ {
		arg := parts[i]
		if strings.HasPrefix(arg, "/") && !strings.HasPrefix(arg, "/dev/") {
			result.ReferencedPaths = append(result.ReferencedPaths, arg)
		}
	}
}

func handleCall(call *syntax.CallExpr, ops *[]FileOperation) {
	if len(call.Args) == 0 {
		return
	}
	cmdName := wordToString(call.Args[0])
	handler, ok := commandHandlers[cmdName]
	if !ok {
		return
	}
	handler(call.Args[1:], int(call.Pos().Line()), ops)
}
