package bash

import "mvdan.cc/sh/v3/syntax"

func handleRedirects(stmt *syntax.Stmt, ops *[]FileOperation) {
	for _, redir := range stmt.Redirs {
		if redir.Word == nil {
			continue
		}
		path := wordToString(redir.Word)
		if path == "" {
			continue
		}

		var op OperationType
		var cmd string
		switch redir.Op {
		case syntax.RdrOut, syntax.RdrAll:
			op = OpCreate
			cmd = ">"
		case syntax.AppOut, syntax.AppAll:
			op = OpModify
			cmd = ">>"
		default:
			continue
		}

		*ops = append(*ops, FileOperation{
			Path:      path,
			Operation: op,
			Command:   cmd,
			Line:      int(redir.Pos().Line()),
			HasGlob:   containsGlob(path),
			HasVar:    containsVar(redir.Word),
		})
	}
}
