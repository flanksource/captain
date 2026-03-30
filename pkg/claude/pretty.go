package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
	"github.com/sergi/go-diff/diffmatchpatch"
)

// Preview line limits matching pi-mono's tool-execution.ts
const (
	BashPreviewLines  = 5
	ReadPreviewLines  = 10
	WritePreviewLines = 10
	GrepPreviewLines  = 15
	FindPreviewLines  = 20
	LsPreviewLines    = 20
	DefaultPreviewMax = 10
)

// toolIcons maps tool names to their display icons
var toolIcons = map[string]icons.Icon{
	"Bash":            {Unicode: "💻", Iconify: "codicon:terminal", Style: "muted"},
	"Read":            icons.File,
	"Write":           {Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"},
	"Edit":            {Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"},
	"MultiEdit":       {Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"},
	"Grep":            icons.Search,
	"Find":            icons.Search,
	"Glob":            icons.Search,
	"Ls":              icons.Folder,
	"WebFetch":        icons.Cloud,
	"WebSearch":       icons.Search,
	"Agent":           {Unicode: "🤖", Iconify: "mdi:robot", Style: "muted"},
	"Task":            icons.Package,
	"TaskCreate":      icons.Package,
	"TodoWrite":       icons.Package,
	"Plan":            {Unicode: "📋", Iconify: "mdi:clipboard-text", Style: "muted"},
	"AskUserQuestion": {Unicode: "❓", Iconify: "mdi:help-circle", Style: "muted"},
	"User":            {Unicode: "💬", Iconify: "mdi:account", Style: "muted"},
	"Skill":           icons.Info,
}

// toolColors maps tool names to their title color class
var toolColors = map[string]string{
	"Bash":            "text-green-400 font-medium",
	"Read":            "text-blue-400 font-medium",
	"Write":           "text-orange-400 font-medium",
	"Edit":            "text-purple-400 font-medium",
	"MultiEdit":       "text-purple-400 font-medium",
	"Grep":            "text-yellow-400 font-medium",
	"Find":            "text-cyan-400 font-medium",
	"Glob":            "text-cyan-400 font-medium",
	"Ls":              "text-blue-400 font-medium",
	"WebFetch":        "text-blue-400 font-medium",
	"WebSearch":       "text-purple-400 font-medium",
	"Agent":           "text-indigo-400 font-medium",
	"Task":            "text-indigo-400 font-medium",
	"TaskCreate":      "text-indigo-400 font-medium",
	"TodoWrite":       "text-indigo-400 font-medium",
	"Plan":            "text-green-400 font-medium",
	"AskUserQuestion": "text-amber-400 font-medium",
	"User":            "text-amber-400 font-medium",
	"Skill":           "text-teal-400 font-medium",
}

// PrettyCommand returns a richly formatted api.Text for the tool use,
// matching pi-mono's tool-execution.ts rendering style.
func (tu ToolUse) PrettyCommand() api.Text {
	icon := toolIcons[tu.Tool]
	color := toolColors[tu.Tool]
	if color == "" {
		color = "text-blue-600 font-medium"
	}

	str := func(key string) string {
		if v, ok := tu.Input[key].(string); ok {
			return v
		}
		return ""
	}

	if tu.isPlanFile() {
		return tu.prettyPlan(str)
	}

	switch tu.Tool {
	case "Bash":
		return tu.prettyBash(icon, color, str)
	case "Read":
		return tu.prettyRead(icon, color, str)
	case "Write":
		return tu.prettyWrite(icon, color, str)
	case "Edit":
		return tu.prettyEdit(icon, color, str)
	case "MultiEdit":
		return tu.prettyMultiEdit(icon, color)
	case "Grep":
		return tu.prettyGrep(icon, color, str)
	case "Find":
		return tu.prettyFind(icon, color, str)
	case "Glob":
		return tu.prettyGlob(icon, color, str)
	case "Ls":
		return tu.prettyLs(icon, color, str)
	case "WebFetch":
		return tu.prettyWebFetch(icon, color, str)
	case "WebSearch":
		return tu.prettyWebSearch(icon, color, str)
	case "Agent", "Task":
		return tu.prettyTask(icon, color, str)
	case "TaskCreate":
		return tu.prettyTaskCreate(icon, color, str)
	case "TodoWrite":
		return tu.prettyTodoWrite(icon, color)
	case "AskUserQuestion":
		return tu.prettyAsk(icon, color)
	case "ExitPlanMode":
		return tu.prettyExitPlan()
	case "User":
		return tu.prettyUser(icon, color, str)
	default:
		return tu.prettyGeneric(icon, color)
	}
}

func (tu ToolUse) prettyBash(icon icons.Icon, color string, str func(string) string) api.Text {
	label, lang := "bash", "bash"
	if interp := tu.Interpreter(); interp != "" {
		label = strings.ToLower(interp)
		lang = bash.InterpreterLanguage(interp)
	}
	text := clicky.Text("").Add(icon).Append(" "+label, color)

	cmd := str("command")
	if cmd == "" {
		return text
	}

	if tu.ProjectRoot != "" {
		cmd = strings.ReplaceAll(cmd, tu.ProjectRoot+"/", "")
	}

	// Show timeout if present (value may be in seconds or milliseconds)
	if timeout, ok := tu.Input["timeout"].(float64); ok && timeout > 0 {
		secs := int(timeout)
		if timeout > 1000 {
			secs = int(timeout / 1000)
		}
		text = text.Append(fmt.Sprintf(" (timeout %ds)", secs), "text-gray-500")
	}

	display := cmd
	if lang != "bash" {
		display = bash.ExtractInterpreterBody(cmd)
	}
	text = text.Append(" ", "").Add(clicky.CodeBlock(lang, display))
	return text
}

func (tu ToolUse) prettyRead(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" read", color)

	rawPath := str("file_path")
	if rawPath == "" {
		rawPath = str("path")
	}
	if rawPath == "" {
		return text.Append(" ...", "text-gray-500")
	}

	path := tu.shortenPath(rawPath)
	text = text.Append(" ", "").Append(path, "text-cyan-600 font-medium")

	// Show line range as :start-end (matching pi-mono style)
	offset, _ := tu.Input["offset"].(float64)
	limit, _ := tu.Input["limit"].(float64)
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

	return text
}

func (tu ToolUse) prettyWrite(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" write", color)

	rawPath := str("file_path")
	if rawPath == "" {
		rawPath = str("path")
	}
	if rawPath == "" {
		return text.Append(" ...", "text-gray-500")
	}

	path := tu.shortenPath(rawPath)
	text = text.Append(" ", "").Append(path, "text-cyan-600 font-medium")

	content := str("content")
	if content != "" {
		lines := strings.Split(content, "\n")
		lang := detectLanguage(rawPath)
		preview := content
		if len(lines) > WritePreviewLines {
			preview = strings.Join(lines[:WritePreviewLines], "\n")
			text = text.Append(" ", "").Add(api.NewCode(preview, lang))
			text = text.Append(" ", "").Append(
				fmt.Sprintf("... (%d more lines, %d total)", len(lines)-WritePreviewLines, len(lines)),
				"text-gray-500",
			)
		} else {
			text = text.Append(" ", "").Add(api.NewCode(preview, lang))
		}
	}

	return text
}

func (tu ToolUse) prettyEdit(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" edit", color)

	rawPath := str("file_path")
	if rawPath == "" {
		rawPath = str("path")
	}
	if rawPath == "" {
		return text.Append(" ...", "text-gray-500")
	}

	path := tu.shortenPath(rawPath)

	oldStr := str("old_string")
	newStr := str("new_string")

	// Compute first changed line number for :line indicator
	if oldStr != "" && newStr != "" {
		firstLine := computeFirstChangedLine(oldStr)
		if firstLine > 0 {
			text = text.Append(" ", "").Append(path, "text-cyan-600 font-medium")
			text = text.Append(fmt.Sprintf(":%d", firstLine), "text-yellow-600")
		} else {
			text = text.Append(" ", "").Append(path, "text-cyan-600 font-medium")
		}
		text = text.Append(" ", "").Add(createUnifiedDiff(oldStr, newStr))
	} else {
		text = text.Append(" ", "").Append(path, "text-cyan-600 font-medium")
	}

	return text
}

func (tu ToolUse) prettyMultiEdit(icon icons.Icon, color string) api.Text {
	text := clicky.Text("").Add(icon).Append(" multi-edit", color)

	if edits, ok := tu.Input["edits"].([]any); ok && len(edits) > 0 {
		text = text.Append(fmt.Sprintf(" (%d edits)", len(edits)), "text-gray-500")
	}

	return text
}

func (tu ToolUse) prettyGrep(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" grep", color)

	pattern := str("pattern")
	searchPath := str("path")

	// Display pattern in /pattern/ notation (matching pi-mono)
	if pattern != "" {
		text = text.Append(" ", "").Append("/"+pattern+"/", "text-cyan-600 font-medium")
	}

	if searchPath != "" {
		path := tu.shortenPath(searchPath)
		text = text.Append(" in ", "text-gray-500").Append(path, "text-gray-400")
	}

	if glob := str("glob"); glob != "" {
		text = text.Append(" (", "text-gray-400").Append(glob, "text-gray-500").Append(")", "text-gray-400")
	}

	if limit, ok := tu.Input["limit"].(float64); ok && limit > 0 {
		text = text.Append(fmt.Sprintf(" limit %d", int(limit)), "text-gray-500")
	}

	return text
}

func (tu ToolUse) prettyFind(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" find", color)

	pattern := str("pattern")
	searchPath := str("path")

	if pattern != "" {
		text = text.Append(" ", "").Append(pattern, "text-cyan-600 font-medium")
	}

	if searchPath != "" {
		path := tu.shortenPath(searchPath)
		text = text.Append(" in ", "text-gray-500").Append(path, "text-gray-400")
	}

	if limit, ok := tu.Input["limit"].(float64); ok && limit > 0 {
		text = text.Append(fmt.Sprintf(" (limit %d)", int(limit)), "text-gray-500")
	}

	return text
}

func (tu ToolUse) prettyGlob(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" glob", color)

	pattern := str("pattern")
	if pattern != "" {
		text = text.Append(" ", "").Append(pattern, "text-cyan-600 font-medium")
	}

	return text
}

func (tu ToolUse) prettyLs(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" ls", color)

	path := str("path")
	if path == "" {
		path = "."
	}
	text = text.Append(" ", "").Append(tu.shortenPath(path), "text-cyan-600 font-medium")

	if limit, ok := tu.Input["limit"].(float64); ok && limit > 0 {
		text = text.Append(fmt.Sprintf(" (limit %d)", int(limit)), "text-gray-500")
	}

	return text
}

func (tu ToolUse) prettyWebFetch(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" web-fetch", color)

	if url := str("url"); url != "" {
		text = text.Append(": ", "text-gray-500").Append(url, "text-blue-400 underline")
	}

	if prompt := str("prompt"); prompt != "" {
		if len(prompt) > 60 {
			prompt = prompt[:57] + "..."
		}
		text = text.Append(" ", "").Append("Prompt: ", "text-gray-500").Append(prompt, "text-gray-400")
	}

	return text
}

func (tu ToolUse) prettyWebSearch(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" web-search", color)

	if query := str("query"); query != "" {
		text = text.Append(": ", "text-gray-500").Append(query, "text-gray-400")
	}

	return text
}

func (tu ToolUse) prettyTask(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" task", color)

	desc := str("description")
	if desc == "" {
		prompt := str("prompt")
		if len(prompt) > 80 {
			desc = prompt[:80] + "..."
		} else {
			desc = prompt
		}
	}

	if desc != "" {
		text = text.Append(": ", "text-gray-400").Append(desc, "text-gray-400")
	}

	if subType := str("subagent_type"); subType != "" {
		text = text.Append(" (", "text-gray-400").Append(subType, "text-gray-500").Append(")", "text-gray-400")
	}

	return text
}

func (tu ToolUse) prettyTaskCreate(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" task", color)
	if subject := str("subject"); subject != "" {
		text = text.Append(": ", "text-gray-400").Append(subject, "text-gray-400")
	}
	return text
}

func (tu ToolUse) prettyTodoWrite(icon icons.Icon, color string) api.Text {
	text := clicky.Text("").Add(icon).Append(" task", color)

	if todos, ok := tu.Input["todos"].([]any); ok {
		text = text.Append(fmt.Sprintf(" (%d items)", len(todos)), "text-gray-500")
	}

	return text
}

func (tu ToolUse) prettyAsk(icon icons.Icon, color string) api.Text {
	text := clicky.Text("").Add(icon).Append(" ask", color)
	question := tu.firstQuestion()
	if question != "" {
		if len(question) > 50 {
			question = question[:50] + "..."
		}
		text = text.Append(" "+question, "")
	}
	return text
}


func (tu ToolUse) prettyExitPlan() api.Text {
	icon := toolIcons["Plan"]
	color := toolColors["Plan"]
	text := clicky.Text("").Add(icon).Append(" plan", color)
	if tu.Denied {
		text = text.Append(" ✗ disapproved", "text-red-600")
	} else {
		text = text.Append(" ✓ approved", "text-green-600")
	}
	return text
}

func (tu ToolUse) prettyPlan(str func(string) string) api.Text {
	icon := toolIcons["Plan"]
	color := toolColors["Plan"]
	text := clicky.Text("").Add(icon).Append(" plan", color)

	filePath := tu.FilePath()
	name := filepath.Base(filePath)
	name = strings.TrimSuffix(name, ".md")
	text = text.Append(" "+name, "text-cyan-600 font-medium")

	// Extract plan title from content (first # heading)
	content := str("content")
	if content == "" {
		content = str("new_string")
	}
	if content != "" {
		for _, line := range strings.SplitN(content, "\n", 10) {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "# ") {
				title := strings.TrimPrefix(line, "# ")
				text = text.Append(" — ", "text-gray-400").Append(title, "max-w-[tw-30ch]")
				break
			}
		}
	}
	return text
}

func (tu ToolUse) prettyUser(icon icons.Icon, color string, str func(string) string) api.Text {
	text := clicky.Text("").Add(icon).Append(" user", color)
	if t := str("text"); t != "" {
		text = text.Append(" "+t, "max-w-[tw-20ch]")
	}
	return text
}

func (tu ToolUse) prettyGeneric(icon icons.Icon, color string) api.Text {
	if icon == (icons.Icon{}) {
		icon = icons.ArrowRight
	}

	text := clicky.Text("").Add(icon).Append(" "+tu.Tool, color)

	// Display input as a clean key-value summary
	if len(tu.Input) > 0 {
		// Build a cleaned input map (skip very long values)
		cleaned := make(map[string]any)
		for k, v := range tu.Input {
			if s, ok := v.(string); ok && len(s) > 100 {
				cleaned[k] = s[:97] + "..."
			} else {
				cleaned[k] = v
			}
		}
		text = text.Append(" ", "").Add(clicky.Map(cleaned, "max-w-[100ch]"))
	}

	return text
}

// shortenPath converts absolute paths to relative or tilde notation.
// Uses project root for relative paths, falls back to ~/... for home dir paths.
func (tu ToolUse) shortenPath(path string) string {
	if path == "" {
		return path
	}

	// Try relative to project root first
	if tu.ProjectRoot != "" {
		rel := RelativePath(path, tu.ProjectRoot)
		if rel != path { // successfully made relative
			return rel
		}
	}

	// Try relative to cwd
	if tu.CWD != "" {
		if rel, err := filepath.Rel(tu.CWD, path); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}

	// Fall back to tilde notation for home directory paths
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}

	return path
}

// detectLanguage returns the syntax highlighting language for a file path
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

// computeFirstChangedLine estimates the line number of the first change.
// Since we don't have the full file, this returns the line count in oldStr
// as an approximate indicator (useful for :line display).
func computeFirstChangedLine(oldStr string) int {
	if oldStr == "" {
		return 0
	}
	// Count lines in oldStr - the first line is where the change begins
	lines := strings.Split(oldStr, "\n")
	if len(lines) > 0 {
		return 1
	}
	return 0
}

// createUnifiedDiff creates a line-based unified diff with line numbers,
// matching pi-mono's diff.ts rendering style.
func createUnifiedDiff(oldStr, newStr string) api.Text {
	oldLines := strings.Split(oldStr, "\n")
	newLines := strings.Split(newStr, "\n")

	dmp := diffmatchpatch.New()
	diffs := dmp.DiffMain(oldStr, newStr, true)
	diffs = dmp.DiffCleanupSemantic(diffs)

	// Convert character-level diffs to line-level display
	result := clicky.Text("")

	// Track positions in old and new text
	var removedLines []string
	var addedLines []string
	var contextLines []string

	// Process diffs and collect lines
	oldLineNum := 1
	newLineNum := 1

	for _, diff := range diffs {
		lines := strings.Split(diff.Text, "\n")
		for i, line := range lines {
			// Skip the last empty element from split
			if i == len(lines)-1 && line == "" && len(lines) > 1 {
				continue
			}

			switch diff.Type {
			case diffmatchpatch.DiffEqual:
				// Flush pending removed/added lines
				result = flushDiffLines(result, removedLines, addedLines, &oldLineNum, &newLineNum)
				removedLines = nil
				addedLines = nil

				// Only show context lines near changes (3 lines window)
				contextLines = append(contextLines, line)
				if len(contextLines) > 3 {
					contextLines = contextLines[len(contextLines)-3:]
				}
				oldLineNum++
				newLineNum++

			case diffmatchpatch.DiffDelete:
				// Show any trailing context from before
				if len(removedLines) == 0 && len(addedLines) == 0 {
					for _, cl := range contextLines {
						result = result.Append(fmt.Sprintf(" %s", cl), "text-gray-400").NewLine()
					}
					contextLines = nil
				}
				removedLines = append(removedLines, line)

			case diffmatchpatch.DiffInsert:
				if len(removedLines) == 0 && len(addedLines) == 0 && len(contextLines) > 0 {
					for _, cl := range contextLines {
						result = result.Append(fmt.Sprintf(" %s", cl), "text-gray-400").NewLine()
					}
					contextLines = nil
				}
				addedLines = append(addedLines, line)
			}
		}
	}

	// Flush remaining
	result = flushDiffLines(result, removedLines, addedLines, &oldLineNum, &newLineNum)

	_ = oldLines
	_ = newLines

	return result
}

// flushDiffLines renders pending removed/added lines with intra-line highlighting
// when there's a 1:1 correspondence between removed and added lines.
func flushDiffLines(result api.Text, removedLines, addedLines []string, oldLineNum, newLineNum *int) api.Text {
	if len(removedLines) == 0 && len(addedLines) == 0 {
		return result
	}

	// Single line change: show intra-line diff with word-level highlighting
	if len(removedLines) == 1 && len(addedLines) == 1 {
		result = result.
			Append(fmt.Sprintf("-%d ", *oldLineNum), "text-red-500").
			Append(removedLines[0], "text-red-500").NewLine().
			Append(fmt.Sprintf("+%d ", *newLineNum), "text-green-500").
			Append(addedLines[0], "text-green-500").NewLine()
		*oldLineNum++
		*newLineNum++
		return result
	}

	// Multiple lines: show all removed, then all added
	for _, line := range removedLines {
		result = result.
			Append(fmt.Sprintf("-%d ", *oldLineNum), "text-red-500").
			Append(line, "text-red-500").NewLine()
		*oldLineNum++
	}
	for _, line := range addedLines {
		result = result.
			Append(fmt.Sprintf("+%d ", *newLineNum), "text-green-500").
			Append(line, "text-green-500").NewLine()
		*newLineNum++
	}

	return result
}

// PrettyHeader returns just the icon + tool label + one-line command summary.
// maxWidth limits the command portion; 0 means no limit.
func (tu ToolUse) PrettyHeaderWith(maxWidth int) api.Text {
	icon := toolIcons[tu.Tool]
	color := toolColors[tu.Tool]
	if color == "" {
		color = "text-blue-600 font-medium"
	}
	label := strings.ToLower(tu.DisplayTool())
	text := clicky.Text("").Add(icon).Append(" "+label, color)
	cmd := tu.FormatCommand()
	if cmd == "" {
		return text
	}
	style := ""
	if maxWidth > 0 {
		style = fmt.Sprintf("max-w-[%dch]", maxWidth)
	}
	text = text.Append(" "+cmd, style)
	return text
}

func (tu ToolUse) PrettyHeader() api.Text {
	icon := toolIcons[tu.Tool]
	color := toolColors[tu.Tool]
	if color == "" {
		color = "text-blue-600 font-medium"
	}
	label := strings.ToLower(tu.DisplayTool())
	text := clicky.Text("").Add(icon).Append(" "+label, color)
	cmd := tu.FormatCommand()
	if cmd != "" {
		text = text.Append(" "+cmd, "max-w-[tw-20ch]")
	}
	return text
}

// PrettyDetail returns the expanded body (code blocks, diffs) without the header.
// Returns nil for tools that have no expanded content.
func (tu ToolUse) PrettyDetail() api.Textable {
	str := func(key string) string {
		if v, ok := tu.Input[key].(string); ok {
			return v
		}
		return ""
	}

	if tu.isPlanFile() {
		content := str("content")
		if content == "" {
			content = str("new_string")
		}
		if content == "" {
			return nil
		}
		return api.NewCode(content, "markdown")
	}

	switch tu.Tool {
	case "Bash":
		cmd := str("command")
		if cmd == "" {
			return nil
		}
		if tu.ProjectRoot != "" {
			cmd = strings.ReplaceAll(cmd, tu.ProjectRoot+"/", "")
		}
		lang := "bash"
		if interp := tu.Interpreter(); interp != "" {
			lang = bash.InterpreterLanguage(interp)
			cmd = bash.ExtractInterpreterBody(cmd)
		}
		return api.NewCode(cmd, lang)

	case "Write":
		content := str("content")
		if content == "" {
			return nil
		}
		lines := strings.Split(content, "\n")
		lang := detectLanguage(str("file_path"))
		if len(lines) > WritePreviewLines {
			content = strings.Join(lines[:WritePreviewLines], "\n")
		}
		return api.NewCode(content, lang)

	case "Edit":
		oldStr, newStr := str("old_string"), str("new_string")
		if oldStr == "" || newStr == "" {
			return nil
		}
		text := createUnifiedDiff(oldStr, newStr)
		return &text

	case "AskUserQuestion":
		text := clicky.Text("")
		q := tu.firstQuestion()
		if q != "" {
			text = text.Append("Q: ", "font-bold").Append(q, "")
		}
		if tu.Response != "" {
			if q != "" {
				text = text.NewLine()
			}
			text = text.Append("A: ", "font-bold text-green-600").Append(tu.Response, "")
		}
		if text.String() == "" {
			return nil
		}
		return &text

	case "Task":
		prompt := str("prompt")
		if prompt == "" {
			return nil
		}
		if len(prompt) > 500 {
			prompt = prompt[:500] + "..."
		}
		return api.NewCode(prompt, "")

	case "ExitPlanMode":
		if prompts, ok := tu.Input["allowedPrompts"].([]any); ok && len(prompts) > 0 {
			var lines []string
			for _, p := range prompts {
				if pm, ok := p.(map[string]any); ok {
					prompt, _ := pm["prompt"].(string)
					tool, _ := pm["tool"].(string)
					if prompt != "" {
						if tool != "" {
							lines = append(lines, tool+": "+prompt)
						} else {
							lines = append(lines, prompt)
						}
					}
				}
			}
			if len(lines) > 0 {
				text := clicky.Text("").Append("Allowed:", "font-bold").NewLine()
				for _, l := range lines {
					text = text.Append("  • "+l, "").NewLine()
				}
				return &text
			}
		}
		return nil

	default:
		if tu.Denied && tu.DeniedReason != "" {
			text := clicky.Text("").Append("User: ", "font-bold text-red-600").Append(tu.DeniedReason, "")
			return &text
		}
		return nil
	}
}

// PrettyTimestamp returns a formatted timestamp string
func (tu ToolUse) PrettyTimestamp() string {
	if tu.Timestamp == nil {
		return ""
	}
	return FormatTimeAgo(tu.Timestamp)
}

// FormatTimeAgo returns a human-readable time ago string
func FormatTimeAgo(t *time.Time) string {
	return api.TimeAgo(t).String()
}
