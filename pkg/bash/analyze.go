package bash

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

func Analyze(script string) (*AnalysisResult, error) {
	parser := syntax.NewParser(syntax.Variant(syntax.LangBash))
	file, err := parser.Parse(strings.NewReader(script), "")
	if err != nil {
		return nil, err
	}

	var ops []FileOperation
	syntax.Walk(file, func(node syntax.Node) bool {
		if stmt, ok := node.(*syntax.Stmt); ok {
			handleRedirects(stmt, &ops)
		}
		if call, ok := node.(*syntax.CallExpr); ok {
			handleCall(call, &ops)
		}
		return true
	})

	return &AnalysisResult{Operations: ops}, nil
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
