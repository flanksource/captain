package history

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
	"github.com/flanksource/commons/logger"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// Preview line limits matching pi-mono's tool-execution.ts
const (
	BashPreviewLines  = 5
	ReadPreviewLines  = 10
	WritePreviewLines = 10
	GrepPreviewLines  = 15
	DefaultPreview    = 10
)

type ToolUseResult struct {
	ToolUse ToolUse
	Index   int
}

func (t ToolUse) Pretty() api.Text {
	cwd, _ := os.Getwd()
	text := clicky.Text("")

	icon := toolIcon(t.Tool)
	color := toolColor(t.Tool)

	text = text.Add(icon).Append(" "+strings.ToLower(t.Tool), color)

	if t.Timestamp != nil || t.CWD != "" {
		text = text.NewLine()
	}
	if t.Timestamp != nil {
		if time.Since(*t.Timestamp).Hours() < 24 {
			text = text.Append(t.Timestamp.Format("15:04:05")+"  ", "text-gray-500")
		} else {
			text = text.Append(t.Timestamp.Format("2006-01-02")+"  ", "text-gray-500")
		}
	}
	if logger.IsDebugEnabled() && t.CWD != "" {
		text = text.Add(icons.Folder).Append(" "+getRelativePath(t.CWD, cwd), "text-gray-400 text-xs")
	}

	fp, _ := t.Input["file_path"].(string)
	if fp == "" {
		fp, _ = t.Input["path"].(string)
	}
	data := copyMap(t.Input)

	if desc, ok := data["description"].(string); ok && desc != "" {
		text = text.Append(": ", "text-gray-400").Append(desc, "text-gray-700")
		delete(data, "description")
	}
	if timeout, ok := data["timeout"].(float64); ok && timeout > 0 {
		data["timeout"] = time.Duration(timeout) * time.Millisecond
	}
	if fp != "" {
		delete(data, "file_path")
		delete(data, "path")
		fp = shortenPath(fp, cwd)
	}

	switch t.Tool {
	case "Bash":
		if cmd, ok := t.Input["command"].(string); ok {
			if timeout, ok := t.Input["timeout"].(float64); ok && timeout > 0 {
				secs := int(timeout)
				if timeout > 1000 {
					secs = int(timeout / 1000)
				}
				text = text.Append(fmt.Sprintf(" (timeout %ds)", secs), "text-gray-500")
			}
			text = text.Add(clicky.CodeBlock("bash", cmd))
		}
		delete(data, "command")
		delete(data, "timeout")

	case "CodexCommand":
		if cmd, ok := t.Input["command"].(string); ok && cmd != "" {
			text = text.Add(clicky.CodeBlock("bash", cmd))
		}
		if output, ok := t.Input["output"].(string); ok && output != "" {
			lines := strings.Split(output, "\n")
			preview := output
			if len(lines) > BashPreviewLines {
				preview = strings.Join(lines[:BashPreviewLines], "\n") +
					fmt.Sprintf("\n... (%d more lines)", len(lines)-BashPreviewLines)
			}
			text = text.NewLine().Add(clicky.CodeBlock("", preview))
		}
		data = nil

	case "CodexReasoning":
		if reasoning, ok := t.Input["text"].(string); ok && reasoning != "" {
			text = clicky.Text("").
				Add(icons.Icon{Unicode: "💭", Iconify: "mdi:thought-bubble", Style: "muted"}).
				Append(" ", "").Append(reasoning, "text-gray-500 italic")
		}
		data = nil

	case "CodexMessage":
		if msg, ok := t.Input["text"].(string); ok && msg != "" {
			text = clicky.Text("").
				Add(icons.Icon{Unicode: "🤖", Iconify: "mdi:robot", Style: "muted"}).
				Append(" Assistant", "text-blue-600 font-medium").
				NewLine().Append(msg, "text-gray-700")
		}
		data = nil

	case "Edit":
		oldStr, _ := t.Input["old_string"].(string)
		newStr, _ := t.Input["new_string"].(string)
		delete(data, "old_string")
		delete(data, "new_string")

		// Show path with :line indicator
		if fp != "" {
			text = text.Append(" ", "").Append(fp, "text-cyan-600 font-medium")
			if oldStr != "" {
				// Approximate line number from old text
				firstLine := 1
				text = text.Append(fmt.Sprintf(":%d", firstLine), "text-yellow-600")
			}
		}

		if oldStr != "" && newStr != "" {
			text = text.NewLine().Add(createUnifiedDiff(oldStr, newStr))
		}

	case "Write":
		content, _ := t.Input["content"].(string)
		delete(data, "content")

		if fp != "" {
			text = text.Append(" ", "").Append(fp, "text-cyan-600 font-medium")
		}

		if content != "" {
			lang := detectLanguage(fp)
			lines := strings.Split(content, "\n")
			preview := content
			if len(lines) > WritePreviewLines {
				preview = strings.Join(lines[:WritePreviewLines], "\n")
				text = text.NewLine().Add(api.NewCode(preview, lang))
				text = text.NewLine().Append(
					fmt.Sprintf("... (%d more lines, %d total)", len(lines)-WritePreviewLines, len(lines)),
					"text-gray-500",
				)
			} else {
				text = text.NewLine().Add(api.NewCode(preview, lang))
			}
		}

	case "Read":
		delete(data, "limit")
		delete(data, "offset")

		if fp != "" {
			text = text.Append(" ", "").Append(fp, "text-cyan-600 font-medium")
		}

		// Show line range as :start-end
		offset, _ := t.Input["offset"].(float64)
		limit, _ := t.Input["limit"].(float64)
		if offset > 0 || limit > 0 {
			startLine := int(offset)
			if startLine == 0 {
				startLine = 1
			}
			if limit > 0 {
				endLine := startLine + int(limit) - 1
				text = text.Append(fmt.Sprintf(":%d-%d", startLine, endLine), "text-yellow-600")
			} else {
				text = text.Append(fmt.Sprintf(":%d", startLine), "text-yellow-600")
			}
		}

	case "Grep":
		pattern, _ := t.Input["pattern"].(string)
		path, _ := t.Input["path"].(string)
		delete(data, "pattern")
		delete(data, "path")

		// Display pattern in /pattern/ notation
		if pattern != "" {
			text = text.Append(" ", "").Append("/"+pattern+"/", "text-cyan-600 font-medium")
		}
		if path != "" {
			text = text.Append(" in ", "text-gray-500").Append(shortenPath(path, cwd), "text-gray-700")
		}
		if glob, ok := data["glob"].(string); ok && glob != "" {
			text = text.Append(" (", "text-gray-400").Append(glob, "text-gray-500").Append(")", "text-gray-400")
			delete(data, "glob")
		}

	case "Glob":
		if pattern, ok := t.Input["pattern"].(string); ok {
			text = text.Append(" ", "").Append(pattern, "text-cyan-600 font-medium")
		}
		delete(data, "path")
		delete(data, "pattern")

	case "WebFetch":
		if url, ok := t.Input["url"].(string); ok {
			text = text.Append(": ", "text-gray-600").Append(url, "text-blue-700 underline")
		}
		delete(data, "url")
		if prompt, ok := data["prompt"].(string); ok && prompt != "" {
			if len(prompt) > 60 {
				prompt = prompt[:57] + "..."
			}
			text = text.NewLine().Append("Prompt: ", "text-gray-500").Append(prompt, "text-gray-700")
			delete(data, "prompt")
		}

	case "WebSearch":
		if query, ok := t.Input["query"].(string); ok {
			text = text.Append(": ", "text-gray-600").Append(query, "text-gray-800")
		}
		delete(data, "query")

	case "Task":
		desc, _ := t.Input["description"].(string)
		if desc == "" {
			if prompt, ok := t.Input["prompt"].(string); ok && len(prompt) > 80 {
				desc = prompt[:80] + "..."
			} else {
				desc, _ = t.Input["prompt"].(string)
			}
		}
		if desc != "" {
			text = text.Append(": ", "text-gray-400").Append(desc, "text-gray-700")
		}
		if subType, ok := t.Input["subagent_type"].(string); ok && subType != "" {
			text = text.Append(" (", "text-gray-400").Append(subType, "text-gray-500").Append(")", "text-gray-400")
		}
		data = nil

	case "TodoWrite":
		if todos, ok := t.Input["todos"].([]any); ok {
			text = text.Append(fmt.Sprintf(" (%d items)", len(todos)), "text-gray-500")
		}
		data = nil

	default:
		// Generic fallback with clean key-value summary
		if len(data) > 0 {
			// Truncate long string values for generic display
			cleaned := make(map[string]any)
			for k, v := range data {
				if s, ok := v.(string); ok && len(s) > 100 {
					cleaned[k] = s[:97] + "..."
				} else {
					cleaned[k] = v
				}
			}
			data = cleaned
		}
	}

	if len(data) > 0 {
		text = text.Add(clicky.Map(data, "max-w-[100ch]"))
	}
	return text
}

type ToolUseSummary struct {
	TotalCount   int
	ToolFilter   string
	LimitApplied int
}

func (s ToolUseSummary) Pretty() api.Text {
	text := clicky.Text("•").Append(fmt.Sprintf(" Found %d commands", s.TotalCount), "font-bold text-blue-600")
	if s.ToolFilter != "" {
		text = text.Append(fmt.Sprintf(" (filtered by %s)", s.ToolFilter), "text-gray-500")
	}
	if s.LimitApplied > 0 && s.TotalCount > s.LimitApplied {
		text = text.Append(fmt.Sprintf("\n  Showing first %d results", s.LimitApplied), "text-yellow-600")
	}
	return text
}

type NoResultsError struct {
	Filter          Filter
	CurrentDir      string
	SearchedAll     bool
	SessionsFound   int
	SessionsScanned int
}

func (e NoResultsError) Pretty() api.Text {
	text := clicky.Text("").
		Add(icons.Error).
		AddText(" No commands found matching criteria", "font-bold text-red-600").
		NewLine().NewLine().
		AddText("Diagnostics:", "font-bold text-yellow-600").
		NewLine()

	if e.SearchedAll {
		text = text.AddText("  • Searched all sessions across all directories", "text-gray-600")
	} else {
		text = text.AddText(fmt.Sprintf("  • Searched current directory: %s", e.CurrentDir), "text-gray-600")
	}

	text = text.NewLine().
		AddText(fmt.Sprintf("  • Sessions found: %d", e.SessionsFound), "text-gray-600").
		NewLine().
		AddText(fmt.Sprintf("  • Sessions scanned: %d", e.SessionsScanned), "text-gray-600")

	if e.Filter.Tool != "" {
		text = text.NewLine().AddText(fmt.Sprintf("  • Tool filter: %s", e.Filter.Tool), "text-gray-600")
	}
	if e.Filter.Limit > 0 {
		text = text.NewLine().AddText(fmt.Sprintf("  • Limit: %d", e.Filter.Limit), "text-gray-600")
	}

	text = text.NewLine().NewLine().
		AddText("Suggestions:", "font-bold text-cyan-600").
		NewLine().
		AddText("  • Try removing filters (e.g., --tool)", "text-cyan-500").
		NewLine().
		AddText("  • Use --all to search all sessions", "text-cyan-500").
		NewLine().
		AddText("  • Increase --limit value", "text-cyan-500").
		NewLine().
		AddText("  • Check if Claude Code has been used recently", "text-cyan-500")

	return text
}

func (e NoResultsError) Error() string {
	if e.Filter.Tool != "" {
		return fmt.Sprintf("no %s commands found in history", e.Filter.Tool)
	}
	return "no commands found in history"
}

// --- Helper functions ---

func toolIcon(tool string) icons.Icon {
	m := map[string]icons.Icon{
		"Bash":         {Unicode: "💻", Iconify: "codicon:terminal", Style: "muted"},
		"CodexCommand": {Unicode: "💻", Iconify: "codicon:terminal", Style: "muted"},
		"Read":         icons.File,
		"Write":        {Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"},
		"Edit":         {Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"},
		"MultiEdit":    {Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"},
		"Grep":         icons.Search,
		"Glob":         icons.Search,
		"WebFetch":     icons.Cloud,
		"WebSearch":    icons.Search,
		"Task":         icons.Package,
		"TodoWrite":    icons.ArrowRight,
		"Skill":        icons.Info,
	}
	if icon, ok := m[tool]; ok {
		return icon
	}
	return icons.ArrowRight
}

func toolColor(tool string) string {
	m := map[string]string{
		"Bash":         "text-green-600 font-medium",
		"CodexCommand": "text-green-600 font-medium",
		"Read":         "text-blue-600 font-medium",
		"Write":        "text-orange-600 font-medium",
		"Edit":         "text-purple-600 font-medium",
		"MultiEdit":    "text-purple-600 font-medium",
		"Grep":         "text-yellow-600 font-medium",
		"Glob":         "text-cyan-600 font-medium",
		"WebFetch":     "text-blue-600 font-medium",
		"WebSearch":    "text-purple-600 font-medium",
		"Task":         "text-indigo-600 font-medium",
		"TodoWrite":    "text-blue-600 font-medium",
		"Skill":        "text-teal-600 font-medium",
	}
	if color, ok := m[tool]; ok {
		return color
	}
	return "text-blue-600 font-medium"
}

func getRelativePath(filePath, workDir string) string {
	if rel, err := filepath.Rel(workDir, filePath); err == nil {
		return rel
	}
	return filePath
}

// shortenPath converts absolute paths to relative or tilde notation.
func shortenPath(path, cwd string) string {
	if path == "" {
		return path
	}

	// Try relative to cwd
	if cwd != "" {
		if rel, err := filepath.Rel(cwd, path); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}

	// Fall back to tilde notation for home directory paths
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}

	return path
}

func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// createUnifiedDiff creates a line-based unified diff with line numbers,
// matching pi-mono's diff.ts rendering style.
func createUnifiedDiff(oldStr, newStr string) api.Text {
	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(oldStr, newStr, true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	result := clicky.Text("")

	// Track line numbers and collect lines by type
	oldLineNum := 1
	newLineNum := 1

	var removedLines []string
	var addedLines []string
	var contextBuf []string

	flushChanges := func() {
		if len(removedLines) == 0 && len(addedLines) == 0 {
			return
		}

		// Show context lines before changes (max 3)
		start := 0
		if len(contextBuf) > 3 {
			start = len(contextBuf) - 3
		}
		for _, cl := range contextBuf[start:] {
			result = result.Append(fmt.Sprintf(" %s", cl), "text-gray-400").NewLine()
		}
		contextBuf = nil

		// Render removed/added with line numbers
		if len(removedLines) == 1 && len(addedLines) == 1 {
			// Single line change: inline diff
			result = result.
				Append(fmt.Sprintf("-%d ", oldLineNum), "text-red-700").
				Append(removedLines[0], "text-red-500").NewLine().
				Append(fmt.Sprintf("+%d ", newLineNum), "text-green-700").
				Append(addedLines[0], "text-green-500").NewLine()
			oldLineNum++
			newLineNum++
		} else {
			for _, line := range removedLines {
				result = result.
					Append(fmt.Sprintf("-%d ", oldLineNum), "text-red-700").
					Append(line, "text-red-500").NewLine()
				oldLineNum++
			}
			for _, line := range addedLines {
				result = result.
					Append(fmt.Sprintf("+%d ", newLineNum), "text-green-700").
					Append(line, "text-green-500").NewLine()
				newLineNum++
			}
		}
		removedLines = nil
		addedLines = nil
	}

	for _, diff := range diffs {
		lines := strings.Split(diff.Text, "\n")
		for i, line := range lines {
			if i == len(lines)-1 && line == "" && len(lines) > 1 {
				continue
			}

			switch diff.Type {
			case diffmatchpatch.DiffEqual:
				flushChanges()
				contextBuf = append(contextBuf, line)
				oldLineNum++
				newLineNum++

			case diffmatchpatch.DiffDelete:
				removedLines = append(removedLines, line)

			case diffmatchpatch.DiffInsert:
				addedLines = append(addedLines, line)
			}
		}
	}

	flushChanges()

	return result
}

func detectLanguage(filePath string) string {
	langMap := map[string]string{
		".go": "go", ".py": "python", ".js": "javascript", ".ts": "typescript",
		".tsx": "typescript", ".jsx": "javascript", ".md": "markdown",
		".yaml": "yaml", ".yml": "yaml", ".json": "json",
		".sh": "bash", ".bash": "bash", ".sql": "sql",
		".html": "html", ".css": "css", ".rs": "rust",
		".rb": "ruby", ".java": "java", ".kt": "kotlin",
		".swift": "swift", ".c": "c", ".cpp": "cpp",
		".h": "c", ".hpp": "cpp", ".toml": "toml",
		".xml": "xml", ".tf": "hcl", ".proto": "protobuf",
	}
	if lang, ok := langMap[filepath.Ext(filePath)]; ok {
		return lang
	}
	return ""
}
