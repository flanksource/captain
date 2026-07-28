package tools

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
	"github.com/segmentio/encoding/json"
)

// ApplyPatchTool renders a multi-file Codex apply_patch. Single-file patches are
// normalized into Write/Edit upstream, so this is the 18% that genuinely touch
// more than one file and cannot be expressed as one file operation.
type ApplyPatchTool struct{ BaseTool }

func (t *ApplyPatchTool) Name() string     { return "ApplyPatch" }
func (t *ApplyPatchTool) Category() string { return string(bash.CategoryEdit) }
func (t *ApplyPatchTool) FilePath() string { return "" }

func (t *ApplyPatchTool) ExtractPath() string {
	files := t.files()
	if len(files) == 0 {
		return ""
	}
	return t.Rel(files[0].Path)
}

func (t *ApplyPatchTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"}
	text := t.header(icon, "patch", "text-orange-500 font-medium")
	files := t.files()
	if len(files) == 0 {
		return text
	}
	if len(files) == 1 {
		file := files[0]
		text = text.Append(" "+string(file.Op), applyPatchOpStyle(file.Op)).
			Append(" "+t.Rel(file.Path), "text-gray-500")
		if file.MoveTo != "" {
			text = text.Append(" → "+t.Rel(file.MoveTo), "text-gray-500")
		}
		return text
	}
	// Name the first few files rather than clipping the line to the terminal:
	// a count-plus-overflow stays readable in HTML and markdown too.
	text = text.Append(fmt.Sprintf(" %d files", len(files)), "text-yellow-400")
	for index, file := range files {
		if index == applyPatchNamedFiles {
			text = text.Append(fmt.Sprintf(" +%d more", len(files)-index), "text-gray-500")
			break
		}
		text = text.Append(" "+t.Rel(file.Path), applyPatchOpStyle(file.Op))
	}
	return text
}

// applyPatchNamedFiles is how many paths the one-line preview names before it
// collapses the rest into a count.
const applyPatchNamedFiles = 3

func (t *ApplyPatchTool) Detail() api.Textable {
	files := t.files()
	if len(files) == 0 {
		return nil
	}
	text := clicky.Text("")
	for index, file := range files {
		if index > 0 {
			text = text.NewLine()
		}
		text = text.Append(string(file.Op)+" "+t.Rel(file.Path), "font-bold text-muted")
		if file.MoveTo != "" {
			text = text.Append(" → "+t.Rel(file.MoveTo), "text-muted")
		}
		switch {
		case file.Content != "":
			text = text.NewLine().Append(api.NewCode(file.Content, detectLanguage(file.Path)))
		case file.Old != "" || file.New != "":
			text = text.NewLine().Append(CreateUnifiedDiff(file.Old, file.New))
		}
	}
	return text
}

func (t *ApplyPatchTool) files() []ApplyPatchFile { return ApplyPatchFiles(t.Input) }

// ApplyPatchFiles reads the normalized file list the history layer attaches to an
// apply_patch row. It falls back to re-parsing the raw patch so a row written
// before normalization — or read straight back out of the database as JSON —
// still yields the same operations.
func ApplyPatchFiles(input map[string]any) []ApplyPatchFile {
	if raw, ok := input["files"]; ok {
		encoded, err := json.Marshal(raw)
		if err == nil {
			var files []ApplyPatchFile
			if json.Unmarshal(encoded, &files) == nil {
				return files
			}
		}
	}
	payload, _ := input["input"].(string)
	return ParseApplyPatch(payload)
}

func applyPatchOpStyle(op ApplyPatchOp) string {
	switch op {
	case ApplyPatchAdd:
		return "text-green-500"
	case ApplyPatchDelete:
		return "text-red-500"
	default:
		return "text-gray-500"
	}
}

// CodexExecScriptTool renders a freeform Codex `exec` payload whose JavaScript
// could not be resolved into tool invocations. It exists so that failure is
// visible in the transcript instead of degrading into an opaque JSON dump --
// silence there is what hid a 0.17% parse rate for the life of the feature.
type CodexExecScriptTool struct{ BaseTool }

func (t *CodexExecScriptTool) Name() string     { return "CodexExecScript" }
func (t *CodexExecScriptTool) Category() string { return string(bash.CategoryOther) }
func (t *CodexExecScriptTool) FilePath() string { return "" }
func (t *CodexExecScriptTool) ExtractPath() string {
	return ""
}

func (t *CodexExecScriptTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "⚙️", Iconify: "mdi:script-text", Style: "muted"}
	text := t.header(icon, "exec script", "text-cyan-500 font-medium")
	if script := t.Str("script"); script != "" {
		text = text.Append(fmt.Sprintf(" (%d lines)", strings.Count(script, "\n")+1), "text-gray-500")
	}
	if failure := t.Str("parse_error"); failure != "" {
		text = text.Append(" "+failure, "text-red-500")
	}
	return text
}

func (t *CodexExecScriptTool) Detail() api.Textable {
	script := t.Str("script")
	if script == "" {
		return nil
	}
	return api.NewCode(script, "javascript")
}
