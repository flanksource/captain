package tools

import (
	"fmt"

	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// GrepTool

type GrepTool struct{ BaseTool }

func (t *GrepTool) Name() string     { return "Grep" }
func (t *GrepTool) Category() string { return "" }

func (t *GrepTool) Pretty() api.Text {
	text := t.header(icons.Search, "grep", "text-yellow-400 font-medium")
	pattern := t.Str("pattern")
	path := t.Str("path")
	glob := t.Str("glob")
	limit := t.Float("limit")

	text = text.Append(" /"+pattern+"/", "text-cyan-400")
	if path != "" {
		text = text.Append(" in ", "").Append(t.Rel(path), "text-gray-500 max-w-[tw-20ch]")
	}
	if glob != "" {
		text = text.Append(" ("+glob+")", "text-gray-500")
	}
	if limit > 0 {
		text = text.Append(fmt.Sprintf(" limit %d", int(limit)), "text-gray-500")
	}
	return text
}

func (t *GrepTool) Detail() api.Textable { return nil }
func (t *GrepTool) FilePath() string      { return "" }
func (t *GrepTool) ExtractPath() string   { return t.Rel(t.Str("path")) }

// GlobTool

type GlobTool struct{ BaseTool }

func (t *GlobTool) Name() string     { return "Glob" }
func (t *GlobTool) Category() string { return "" }

func (t *GlobTool) Pretty() api.Text {
	text := t.header(icons.Search, "glob", "text-cyan-400 font-medium")
	pattern := t.Str("pattern")
	return text.Append(" "+pattern, "max-w-[tw-20ch]")
}

func (t *GlobTool) Detail() api.Textable { return nil }
func (t *GlobTool) ExtractPath() string   { return t.Rel(t.Str("path")) }

// FindTool

type FindTool struct{ BaseTool }

func (t *FindTool) Name() string     { return "Find" }
func (t *FindTool) Category() string { return "" }

func (t *FindTool) Pretty() api.Text {
	text := t.header(icons.Search, "find", "text-cyan-400 font-medium")
	pattern := t.Str("pattern")
	path := t.Str("path")
	limit := t.Float("limit")

	text = text.Append(" "+pattern, "max-w-[tw-20ch]")
	if path != "" {
		text = text.Append(" in "+t.Rel(path), "text-gray-500 max-w-[tw-20ch]")
	}
	if limit > 0 {
		text = text.Append(fmt.Sprintf(" limit %d", int(limit)), "text-gray-500")
	}
	return text
}

func (t *FindTool) Detail() api.Textable { return nil }
func (t *FindTool) ExtractPath() string   { return t.Rel(t.Str("path")) }

// LsTool

type LsTool struct{ BaseTool }

func (t *LsTool) Name() string     { return "Ls" }
func (t *LsTool) Category() string { return "" }

func (t *LsTool) Pretty() api.Text {
	text := t.header(icons.Folder, "ls", "text-blue-400 font-medium")
	path := t.Str("path")
	limit := t.Float("limit")

	text = text.Append(" "+t.Rel(path), "max-w-[tw-20ch]")
	if limit > 0 {
		text = text.Append(fmt.Sprintf(" limit %d", int(limit)), "text-gray-500")
	}
	return text
}

func (t *LsTool) Detail() api.Textable { return nil }
func (t *LsTool) ExtractPath() string   { return t.Rel(t.Str("path")) }
