package claude

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/commons/collections"
)

// ToolUse represents a single tool invocation extracted from history
type ToolUse struct {
	Tool        string         `json:"tool,omitempty"`
	Input       map[string]any `json:"input,omitempty"`
	Timestamp   *time.Time     `json:"timestamp,omitempty"`
	CWD         string         `json:"cwd,omitempty"`
	SessionID   string         `json:"session_id,omitempty"`
	ToolUseID   string         `json:"tool_use_id,omitempty"`
	ProjectRoot string         `json:"project_root,omitempty"`
}

// Filter defines criteria for filtering tool uses
type Filter struct {
	Tools  []string
	Dirs   []string
	Since  *time.Time
	Before *time.Time
	Limit  int
}

// ExtractToolUses extracts ToolUse records from history entries
func ExtractToolUses(entries []HistoryEntry) []ToolUse {
	var toolUses []ToolUse

	for _, entry := range entries {
		ts, _ := entry.ParseTimestamp()

		for _, content := range entry.Message.Content {
			if content.Type != ContentTypeToolUse {
				continue
			}

			var inputMap map[string]any
			if content.Input != nil {
				_ = json.Unmarshal(content.Input, &inputMap)
			}

			var timestamp *time.Time
			if !ts.IsZero() {
				timestamp = &ts
			}

			var cwd string
			if inputMap != nil {
				if v, ok := inputMap["cwd"].(string); ok {
					cwd = v
				}
			}

			toolUses = append(toolUses, ToolUse{
				Tool:      content.Name,
				Input:     inputMap,
				Timestamp: timestamp,
				CWD:       cwd,
				SessionID: entry.SessionID,
				ToolUseID: content.ID,
			})
		}
	}

	return toolUses
}

// FilterToolUses applies filter criteria to tool uses
func FilterToolUses(toolUses []ToolUse, filter Filter) []ToolUse {
	var filtered []ToolUse

	for _, tu := range toolUses {
		if len(filter.Tools) > 0 && !collections.MatchItems(tu.Tool, filter.Tools...) {
			continue
		}

		if len(filter.Dirs) > 0 {
			dirToCheck := tu.CWD
			if fp := tu.FilePath(); fp != "" {
				dirToCheck = filepath.Dir(fp)
			}
			if dirToCheck != "" && !collections.MatchItems(dirToCheck, filter.Dirs...) {
				continue
			}
		}

		if filter.Since != nil && tu.Timestamp != nil && tu.Timestamp.Before(*filter.Since) {
			continue
		}

		if filter.Before != nil && tu.Timestamp != nil && tu.Timestamp.After(*filter.Before) {
			continue
		}

		filtered = append(filtered, tu)
	}

	if filter.Limit > 0 {
		sort.Slice(filtered, func(i, j int) bool {
			if filtered[i].Timestamp == nil {
				return false
			}
			if filtered[j].Timestamp == nil {
				return true
			}
			return filtered[i].Timestamp.After(*filtered[j].Timestamp)
		})
		if len(filtered) > filter.Limit {
			filtered = filtered[:filter.Limit]
		}
	}

	return filtered
}

// relativePath makes an absolute path relative to projectRoot if possible.
// For paths outside the project (more than 1 parent level away), returns absolute path.
func relativePath(path, projectRoot string) string {
	if path == "" {
		return path
	}
	if projectRoot == "" {
		return path
	}
	// Path is inside project root
	if strings.HasPrefix(path, projectRoot+"/") {
		return path[len(projectRoot)+1:]
	}
	if strings.HasPrefix(path, projectRoot) {
		return path[len(projectRoot):]
	}
	// Check if path is within 1 parent level of project root
	parentDir := filepath.Dir(projectRoot)
	if strings.HasPrefix(path, parentDir+"/") {
		return "../" + path[len(parentDir)+1:]
	}
	// More than 1 level away - return absolute path
	return path
}

// FormatCommand extracts a human-readable command string from a ToolUse
func (tu ToolUse) FormatCommand() string {
	rel := func(path string) string {
		return relativePath(path, tu.ProjectRoot)
	}

	switch tu.Tool {
	case "Bash":
		if cmd, ok := tu.Input["command"].(string); ok {
			if tu.ProjectRoot != "" {
				return strings.ReplaceAll(cmd, tu.ProjectRoot+"/", "")
			}
			return cmd
		}
	case "Read", "Write", "Edit":
		if path, ok := tu.Input["file_path"].(string); ok {
			return rel(path)
		}
	case "Grep":
		pattern, _ := tu.Input["pattern"].(string)
		path, _ := tu.Input["path"].(string)
		if pattern != "" && path != "" {
			return pattern + " " + rel(path)
		}
		return pattern
	case "Glob":
		if pattern, ok := tu.Input["pattern"].(string); ok {
			return rel(pattern)
		}
	case "WebFetch":
		if url, ok := tu.Input["url"].(string); ok {
			return url
		}
	case "AskUserQuestion":
		if questions, ok := tu.Input["questions"].([]any); ok {
			return fmt.Sprintf("%d questions", len(questions))
		}
	case "ExitPlanMode":
		if plan, ok := tu.Input["plan"].(string); ok {
			if len(plan) > 50 {
				return plan[:50] + "..."
			}
			return plan
		}
		return "exit plan mode"
	case "Task":
		subType, _ := tu.Input["subagent_type"].(string)
		desc, _ := tu.Input["description"].(string)
		if subType != "" && desc != "" {
			return subType + ": " + desc
		}
		if desc != "" {
			return desc
		}
		return subType
	case "TodoWrite":
		if todos, ok := tu.Input["todos"].([]any); ok {
			return fmt.Sprintf("%d todos", len(todos))
		}
	case "WebSearch":
		if query, ok := tu.Input["query"].(string); ok {
			return query
		}
	}

	b, _ := json.Marshal(tu.Input)
	return string(b)
}

// FilePath returns the file_path from tool input, if present
func (tu ToolUse) FilePath() string {
	if path, ok := tu.Input["file_path"].(string); ok {
		return path
	}
	return ""
}

// ExtractPath returns the relevant directory/file path for this tool use
func (tu ToolUse) ExtractPath() string {
	rel := func(path string) string {
		return relativePath(path, tu.ProjectRoot)
	}

	switch tu.Tool {
	case "Read", "Write", "Edit":
		if path, ok := tu.Input["file_path"].(string); ok {
			return rel(path)
		}
	case "Grep", "Glob":
		if path, ok := tu.Input["path"].(string); ok {
			return rel(path)
		}
	case "Bash":
		if cmd, ok := tu.Input["command"].(string); ok {
			if result, err := bash.Analyze(cmd); err == nil && len(result.ReferencedPaths) > 0 {
				return rel(filepath.Dir(result.ReferencedPaths[0]))
			}
		}
	}
	return ""
}
