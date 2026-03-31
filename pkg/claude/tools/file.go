package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

const WritePreviewLines = 10

// ReadTool

type ReadTool struct{ BaseTool }

func (t *ReadTool) Name() string     { return "Read" }
func (t *ReadTool) Category() string { return "" }

func (t *ReadTool) Pretty() api.Text {
	text := t.header(icons.File, "read", "text-blue-400 font-medium")
	path := t.Rel(t.FilePath())
	text = text.Append(" "+path, "text-gray-500 max-w-[tw-20ch]")
	if offset := t.Float("offset"); offset > 0 {
		limit := t.Float("limit")
		if limit > 0 {
			text = text.Append(fmt.Sprintf(":%d-%d", int(offset), int(offset+limit)), "text-yellow-400")
		} else {
			text = text.Append(fmt.Sprintf(":%d", int(offset)), "text-yellow-400")
		}
	}
	return text
}

func (t *ReadTool) Detail() api.Textable   { return nil }
func (t *ReadTool) ExtractPath() string     { return t.Rel(t.FilePath()) }

// WriteTool

type WriteTool struct{ BaseTool }

func (t *WriteTool) Name() string     { return "Write" }
func (t *WriteTool) Category() string { return "" }

func (t *WriteTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "✏️", Iconify: "mdi:file-edit", Style: "muted"}
	text := t.header(icon, "write", "text-orange-400 font-medium")
	path := t.Rel(t.FilePath())
	text = text.Append(" "+path, "text-gray-500 max-w-[tw-20ch]")
	if content := t.Str("content"); content != "" {
		lines := strings.Count(content, "\n") + 1
		text = text.Append(fmt.Sprintf(" (%d lines)", lines), "text-yellow-400")
	}
	return text
}

func (t *WriteTool) Detail() api.Textable {
	content := t.Str("content")
	if content == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	lang := detectLanguage(t.FilePath())
	if len(lines) > WritePreviewLines {
		content = strings.Join(lines[:WritePreviewLines], "\n")
	}
	return api.NewCode(content, lang)
}

func (t *WriteTool) ExtractPath() string { return t.Rel(t.FilePath()) }

// EditTool

type EditTool struct{ BaseTool }

func (t *EditTool) Name() string     { return "Edit" }
func (t *EditTool) Category() string { return "" }

func (t *EditTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "✏️", Iconify: "mdi:file-edit", Style: "muted"}
	text := t.header(icon, "edit", "text-purple-400 font-medium")
	path := t.Rel(t.FilePath())
	text = text.Append(" "+path, "text-gray-500 max-w-[tw-20ch]")
	oldStr, newStr := t.Str("old_string"), t.Str("new_string")
	if oldStr != "" && newStr != "" {
		oldLines := strings.Count(oldStr, "\n") + 1
		newLines := strings.Count(newStr, "\n") + 1
		text = text.Append(fmt.Sprintf(" -%d +%d", oldLines, newLines), "text-yellow-400")
	}
	return text
}

func (t *EditTool) Detail() api.Textable {
	oldStr, newStr := t.Str("old_string"), t.Str("new_string")
	if oldStr == "" || newStr == "" {
		return nil
	}
	d := CreateUnifiedDiff(oldStr, newStr)
	return &d
}

func (t *EditTool) ExtractPath() string { return t.Rel(t.FilePath()) }

// MultiEditTool

type MultiEditTool struct{ BaseTool }

func (t *MultiEditTool) Name() string     { return "Edit" }
func (t *MultiEditTool) Category() string { return "" }

func (t *MultiEditTool) Pretty() api.Text {
	icon := icons.Icon{Unicode: "✏️", Iconify: "mdi:file-edit", Style: "muted"}
	text := t.header(icon, "multi-edit", "text-purple-400 font-medium")
	path := t.Rel(t.FilePath())
	text = text.Append(" "+path, "text-gray-500 max-w-[tw-20ch]")
	if edits, ok := t.Input["edits"].([]any); ok {
		text = text.Append(fmt.Sprintf(" (%d edits)", len(edits)), "text-yellow-400")
	}
	return text
}

func (t *MultiEditTool) Detail() api.Textable   { return nil }
func (t *MultiEditTool) ExtractPath() string     { return t.Rel(t.FilePath()) }

// detectLanguage returns the syntax highlighting language from a file path.
func detectLanguage(path string) string {
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	switch ext {
	case "go":
		return "go"
	case "py":
		return "python"
	case "js":
		return "javascript"
	case "ts":
		return "typescript"
	case "tsx":
		return "tsx"
	case "jsx":
		return "jsx"
	case "rs":
		return "rust"
	case "rb":
		return "ruby"
	case "sh", "bash":
		return "bash"
	case "yaml", "yml":
		return "yaml"
	case "json":
		return "json"
	case "md":
		return "markdown"
	case "sql":
		return "sql"
	case "html":
		return "html"
	case "css":
		return "css"
	default:
		return ext
	}
}
