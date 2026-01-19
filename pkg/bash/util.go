package bash

import (
	"strings"

	"mvdan.cc/sh/v3/syntax"
)

func wordToString(word *syntax.Word) string {
	if word == nil {
		return ""
	}
	var sb strings.Builder
	for _, part := range word.Parts {
		sb.WriteString(partToString(part))
	}
	return sb.String()
}

func partToString(part syntax.WordPart) string {
	switch p := part.(type) {
	case *syntax.Lit:
		return p.Value
	case *syntax.SglQuoted:
		return p.Value
	case *syntax.DblQuoted:
		var sb strings.Builder
		for _, inner := range p.Parts {
			sb.WriteString(partToString(inner))
		}
		return sb.String()
	case *syntax.ParamExp:
		if p.Param != nil {
			return "$" + p.Param.Value
		}
		return "$"
	case *syntax.CmdSubst:
		return "$(...)"
	case *syntax.ArithmExp:
		return "$(())"
	case *syntax.BraceExp:
		return "{...}"
	default:
		return ""
	}
}

func containsGlob(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

func containsVar(word *syntax.Word) bool {
	if word == nil {
		return false
	}
	for _, part := range word.Parts {
		if containsVarPart(part) {
			return true
		}
	}
	return false
}

func containsVarPart(part syntax.WordPart) bool {
	switch p := part.(type) {
	case *syntax.ParamExp, *syntax.CmdSubst, *syntax.ArithmExp:
		return true
	case *syntax.DblQuoted:
		for _, inner := range p.Parts {
			if containsVarPart(inner) {
				return true
			}
		}
	}
	return false
}

func filterFlags(words []*syntax.Word) []*syntax.Word {
	var result []*syntax.Word
	for _, w := range words {
		s := wordToString(w)
		if s != "" && !strings.HasPrefix(s, "-") {
			result = append(result, w)
		}
	}
	return result
}

func hasFlag(words []*syntax.Word, flags ...string) bool {
	for _, w := range words {
		s := wordToString(w)
		for _, flag := range flags {
			if s == flag {
				return true
			}
		}
	}
	return false
}
