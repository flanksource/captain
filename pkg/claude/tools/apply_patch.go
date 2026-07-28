package tools

import (
	"encoding/json"
	"regexp"
	"strings"
)

var javascriptStringRE = regexp.MustCompile(`"(?:\\.|[^"\\])*"`)

// ApplyPatchOp is the operation one file header declares in Codex's V4A patch
// format.
type ApplyPatchOp string

const (
	ApplyPatchAdd    ApplyPatchOp = "add"
	ApplyPatchUpdate ApplyPatchOp = "update"
	ApplyPatchDelete ApplyPatchOp = "delete"
)

// ApplyPatchFile is one file's worth of an apply_patch payload. Old and New are
// the reconstructed before/after text of an update, so an update can render as
// the same diff an Edit produces instead of as a raw patch blob.
type ApplyPatchFile struct {
	Op      ApplyPatchOp `json:"op"`
	Path    string       `json:"path"`
	MoveTo  string       `json:"move_to,omitempty"`
	Content string       `json:"content,omitempty"`
	Old     string       `json:"old,omitempty"`
	New     string       `json:"new,omitempty"`
}

var applyPatchHeaders = map[string]ApplyPatchOp{
	"*** Add File: ":    ApplyPatchAdd,
	"*** Update File: ": ApplyPatchUpdate,
	"*** Delete File: ": ApplyPatchDelete,
}

// ParseApplyPatch decodes a native or JavaScript-wrapped Codex patch into the
// files it touches. It is the single parser for the format: ExtractApplyPatchPaths
// is a projection of it rather than a second, separately-drifting reader.
func ParseApplyPatch(input string) []ApplyPatchFile {
	var files []ApplyPatchFile
	for _, payload := range applyPatchPayloads(input) {
		files = append(files, parseApplyPatchPayload(payload)...)
	}
	return files
}

// ExtractApplyPatchPaths returns every path a patch reads or writes, including
// the destination of a rename.
func ExtractApplyPatchPaths(input string) []string {
	var paths []string
	for _, file := range ParseApplyPatch(input) {
		if file.Path != "" {
			paths = append(paths, file.Path)
		}
		if file.MoveTo != "" {
			paths = append(paths, file.MoveTo)
		}
	}
	return paths
}

func parseApplyPatchPayload(payload string) []ApplyPatchFile {
	var files []ApplyPatchFile
	var current *ApplyPatchFile
	var before, after []string

	flush := func() {
		if current == nil {
			return
		}
		switch current.Op {
		case ApplyPatchAdd:
			current.Content = strings.Join(after, "\n")
		case ApplyPatchUpdate:
			current.Old = strings.Join(before, "\n")
			current.New = strings.Join(after, "\n")
		}
		files = append(files, *current)
		current, before, after = nil, nil, nil
	}

	for _, line := range strings.Split(payload, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if op, path, ok := applyPatchHeader(line); ok {
			flush()
			current = &ApplyPatchFile{Op: op, Path: path}
			continue
		}
		if current == nil {
			continue
		}
		if to, ok := strings.CutPrefix(line, "*** Move to:"); ok {
			current.MoveTo = strings.TrimSpace(to)
			continue
		}
		// `*** End Patch`, `*** End of File` and `@@ ...` are structure, not
		// content; everything else is a diff line.
		if strings.HasPrefix(line, "*** ") || strings.HasPrefix(line, "@@") {
			continue
		}
		switch {
		case strings.HasPrefix(line, "+"):
			after = append(after, line[1:])
		case strings.HasPrefix(line, "-"):
			before = append(before, line[1:])
		default:
			context := strings.TrimPrefix(line, " ")
			before = append(before, context)
			after = append(after, context)
		}
	}
	flush()
	return files
}

func applyPatchHeader(line string) (ApplyPatchOp, string, bool) {
	for prefix, op := range applyPatchHeaders {
		if path, ok := strings.CutPrefix(line, prefix); ok {
			return op, strings.TrimSpace(path), true
		}
	}
	return "", "", false
}

// applyPatchPayloads unwraps the JavaScript form Codex's freeform exec tool
// uses -- tools.apply_patch("*** Begin Patch\n...") -- so the same parser reads
// both shapes.
func applyPatchPayloads(input string) []string {
	if !strings.Contains(input, "tools.apply_patch") {
		return []string{input}
	}
	var payloads []string
	for _, literal := range javascriptStringRE.FindAllString(input, -1) {
		var decoded string
		if json.Unmarshal([]byte(literal), &decoded) == nil && strings.Contains(decoded, "*** Begin Patch") {
			payloads = append(payloads, decoded)
		}
	}
	return payloads
}
