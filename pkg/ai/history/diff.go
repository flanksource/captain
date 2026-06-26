package history

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/sergi/go-diff/diffmatchpatch"
)

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
