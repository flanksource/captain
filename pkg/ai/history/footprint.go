package history

import (
	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude/tools"
)

// Footprint is the file set one tool use touched. Paths are returned exactly as
// the tool expressed them — relative to that tool's own cwd, or absolute — so
// each caller anchors them against the base it needs: `captain changes` resolves
// them absolutely, the session builders relativise against the project root.
type Footprint struct {
	Read    []string
	Written []string
}

// ToolFootprint is the single definition of which files a tool use read and
// wrote. It exists because the answer used to be computed four different ways —
// once for `captain changes`, once per session builder, and once more in a dead
// row projection — so the same session reported different changed files
// depending on which surface asked, and Claude and Codex sessions disagreed on
// the same stored field.
//
// A write always outranks a read: a command that reads a file and then rewrites
// it is a modification, and listing it under both is how the same path ended up
// on both sides of the same session.
func ToolFootprint(tu ToolUse) Footprint {
	var footprint Footprint
	switch tu.Tool {
	case "Read":
		footprint.Read = stringInput(tu, "file_path")
	case "Grep", "Glob":
		footprint.Read = stringInput(tu, "path")
	case "ApplyPatch":
		// A parsed patch keeps its operations on the row, so the paths come from
		// there rather than from re-scanning the payload. A rename writes both
		// ends: git reports both as dirty, so a commit has to attribute both.
		for _, file := range tools.ApplyPatchFiles(tu.Input) {
			footprint.Written = append(footprint.Written, file.Path, file.MoveTo)
		}
	case "CodexExecScript":
		footprint.Written = patchPaths(tu, "script")
	case "Bash":
		footprint = bashFootprint(tu)
	}
	if key, ok := fileMutatingTools[tu.Tool]; ok {
		footprint.Written = append(footprint.Written, stringInput(tu, key)...)
	}
	// Patch payloads are checked for every tool that can carry one, including the
	// shapes handled above: an ApplyPatch row that was never parsed into
	// operations still has its payload, and an agent can pipe a patch through the
	// shell.
	if _, ok := applyPatchInputs[tu.Tool]; ok {
		footprint.Written = append(footprint.Written, patchPaths(tu, applyPatchInputs[tu.Tool])...)
	}
	footprint.Written = cleanPaths(footprint.Written, nil)
	footprint.Read = cleanPaths(footprint.Read, footprint.Written)
	return footprint
}

// bashFootprint covers the writes only a shell command can express — redirects,
// sed -i, mv, rm — which no tool-input lookup can see.
func bashFootprint(tu ToolUse) Footprint {
	command, _ := tu.Input["command"].(string)
	if command == "" {
		return Footprint{}
	}
	result, err := bash.Analyze(command)
	if err != nil || result == nil {
		return Footprint{}
	}
	var footprint Footprint
	for _, operation := range result.Operations {
		footprint.Written = append(footprint.Written, operation.Path)
	}
	footprint.Read = append(footprint.Read, result.ReferencedPaths...)
	return footprint
}

func patchPaths(tu ToolUse, key string) []string {
	payload, _ := tu.Input[key].(string)
	if payload == "" {
		return nil
	}
	return tools.ExtractApplyPatchPaths(payload)
}

func stringInput(tu ToolUse, key string) []string {
	value, _ := tu.Input[key].(string)
	if value == "" {
		return nil
	}
	return []string{value}
}

// cleanPaths drops empties, duplicates, and paths present in exclude, keeping
// first-seen order. /dev/null is dropped outright: a patch that creates or
// deletes a file names it as the empty side of the hunk, and it is not a file
// any surface should report as changed.
func cleanPaths(paths []string, exclude []string) []string {
	if len(paths) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(paths)+len(exclude))
	for _, path := range exclude {
		seen[path] = struct{}{}
	}
	cleaned := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" || path == "/dev/null" {
			continue
		}
		if _, dup := seen[path]; dup {
			continue
		}
		seen[path] = struct{}{}
		cleaned = append(cleaned, path)
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}
