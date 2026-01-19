package bash

import "mvdan.cc/sh/v3/syntax"

type commandHandler func(args []*syntax.Word, line int, ops *[]FileOperation)

var commandHandlers = map[string]commandHandler{
	"touch": handleTouch,
	"mkdir": handleMkdir,
	"rm":    handleRm,
	"rmdir": handleRmdir,
	"cp":    handleCp,
	"mv":    handleMv,
	"chmod": handleChmod,
	"chown": handleChown,
	"tee":   handleTee,
	"sed":   handleSed,
}

func handleTouch(args []*syntax.Word, line int, ops *[]FileOperation) {
	for _, w := range filterFlags(args) {
		path := wordToString(w)
		*ops = append(*ops, FileOperation{
			Path:      path,
			Operation: OpCreate,
			Command:   "touch",
			Line:      line,
			HasGlob:   containsGlob(path),
			HasVar:    containsVar(w),
		})
	}
}

func handleMkdir(args []*syntax.Word, line int, ops *[]FileOperation) {
	for _, w := range filterFlags(args) {
		path := wordToString(w)
		*ops = append(*ops, FileOperation{
			Path:      path,
			Operation: OpCreate,
			Command:   "mkdir",
			Line:      line,
			HasGlob:   containsGlob(path),
			HasVar:    containsVar(w),
		})
	}
}

func handleRm(args []*syntax.Word, line int, ops *[]FileOperation) {
	for _, w := range filterFlags(args) {
		path := wordToString(w)
		*ops = append(*ops, FileOperation{
			Path:      path,
			Operation: OpDelete,
			Command:   "rm",
			Line:      line,
			HasGlob:   containsGlob(path),
			HasVar:    containsVar(w),
		})
	}
}

func handleRmdir(args []*syntax.Word, line int, ops *[]FileOperation) {
	for _, w := range filterFlags(args) {
		path := wordToString(w)
		*ops = append(*ops, FileOperation{
			Path:      path,
			Operation: OpDelete,
			Command:   "rmdir",
			Line:      line,
			HasGlob:   containsGlob(path),
			HasVar:    containsVar(w),
		})
	}
}

func handleCp(args []*syntax.Word, line int, ops *[]FileOperation) {
	filtered := filterFlags(args)
	if len(filtered) < 2 {
		return
	}
	dest := filtered[len(filtered)-1]
	path := wordToString(dest)
	*ops = append(*ops, FileOperation{
		Path:      path,
		Operation: OpCreate,
		Command:   "cp",
		Line:      line,
		HasGlob:   containsGlob(path),
		HasVar:    containsVar(dest),
	})
}

func handleMv(args []*syntax.Word, line int, ops *[]FileOperation) {
	filtered := filterFlags(args)
	if len(filtered) < 2 {
		return
	}
	src := filtered[0]
	dest := filtered[len(filtered)-1]
	srcPath := wordToString(src)
	destPath := wordToString(dest)

	*ops = append(*ops, FileOperation{
		Path:      srcPath,
		Operation: OpDelete,
		Command:   "mv",
		Line:      line,
		HasGlob:   containsGlob(srcPath),
		HasVar:    containsVar(src),
	})
	*ops = append(*ops, FileOperation{
		Path:      destPath,
		Operation: OpCreate,
		Command:   "mv",
		Line:      line,
		HasGlob:   containsGlob(destPath),
		HasVar:    containsVar(dest),
	})
}

func handleChmod(args []*syntax.Word, line int, ops *[]FileOperation) {
	filtered := filterFlags(args)
	if len(filtered) < 2 {
		return
	}
	for _, w := range filtered[1:] {
		path := wordToString(w)
		*ops = append(*ops, FileOperation{
			Path:      path,
			Operation: OpModify,
			Command:   "chmod",
			Line:      line,
			HasGlob:   containsGlob(path),
			HasVar:    containsVar(w),
		})
	}
}

func handleChown(args []*syntax.Word, line int, ops *[]FileOperation) {
	filtered := filterFlags(args)
	if len(filtered) < 2 {
		return
	}
	for _, w := range filtered[1:] {
		path := wordToString(w)
		*ops = append(*ops, FileOperation{
			Path:      path,
			Operation: OpModify,
			Command:   "chown",
			Line:      line,
			HasGlob:   containsGlob(path),
			HasVar:    containsVar(w),
		})
	}
}

func handleTee(args []*syntax.Word, line int, ops *[]FileOperation) {
	appendMode := hasFlag(args, "-a")
	op := OpCreate
	if appendMode {
		op = OpModify
	}
	for _, w := range filterFlags(args) {
		path := wordToString(w)
		*ops = append(*ops, FileOperation{
			Path:      path,
			Operation: op,
			Command:   "tee",
			Line:      line,
			HasGlob:   containsGlob(path),
			HasVar:    containsVar(w),
		})
	}
}

func handleSed(args []*syntax.Word, line int, ops *[]FileOperation) {
	if !hasFlag(args, "-i") {
		return
	}
	filtered := filterFlags(args)
	if len(filtered) < 2 {
		return
	}
	for _, w := range filtered[1:] {
		path := wordToString(w)
		*ops = append(*ops, FileOperation{
			Path:      path,
			Operation: OpModify,
			Command:   "sed",
			Line:      line,
			HasGlob:   containsGlob(path),
			HasVar:    containsVar(w),
		})
	}
}
