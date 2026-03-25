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
	Tool         string         `json:"tool,omitempty"`
	Input        map[string]any `json:"input,omitempty"`
	Timestamp    *time.Time     `json:"timestamp,omitempty"`
	CWD          string         `json:"cwd,omitempty"`
	SessionID    string         `json:"session_id,omitempty"`
	ToolUseID    string         `json:"tool_use_id,omitempty"`
	ProjectRoot  string         `json:"project_root,omitempty"`
	Denied       bool           `json:"denied,omitempty"`
	DeniedReason string         `json:"deniedReason,omitempty"`
	InputTokens  int            `json:"inputTokens,omitempty"`
	OutputTokens int            `json:"outputTokens,omitempty"`
	IsError      bool           `json:"isError,omitempty"`
}

// Filter defines criteria for filtering tool uses
type Filter struct {
	Tools  []string
	Dirs   []string
	Since  *time.Time
	Before *time.Time
	Limit  int
}

const denialPrefix = "The user doesn't want to proceed with this tool use."
const denialCommentSeparator = "the user said:\n"

// ExtractToolUses extracts ToolUse records from history entries
func ExtractToolUses(entries []HistoryEntry) []ToolUse {
	var toolUses []ToolUse

	// Pass 1: extract tool_use blocks
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

	// Pass 2: scan user messages for denied tool_results
	denials := buildDenialMap(entries)
	for i := range toolUses {
		if reason, ok := denials[toolUses[i].ToolUseID]; ok {
			toolUses[i].Denied = true
			toolUses[i].DeniedReason = reason
		}
	}

	return toolUses
}

// toolResult holds matched result data for a tool call.
type toolResult struct {
	content json.RawMessage
	isError bool
}

// ExtractToolUsesWithTokens extracts tool uses and links them to their results,
// estimating token consumption for each call input and result output.
func ExtractToolUsesWithTokens(entries []HistoryEntry) []ToolUse {
	toolUses := ExtractToolUses(entries)

	// Build map of toolUseID → result content from user messages
	results := make(map[string]toolResult)
	for _, entry := range entries {
		if entry.Message.Role != MessageRoleUser {
			continue
		}
		for _, block := range entry.Message.Content {
			if block.Type != ContentTypeToolResult || block.ToolUseID == "" {
				continue
			}
			results[block.ToolUseID] = toolResult{
				content: block.Content,
				isError: block.IsError,
			}
		}
	}

	for i := range toolUses {
		// Estimate input tokens from tool call input
		if raw, err := json.Marshal(toolUses[i].Input); err == nil {
			toolUses[i].InputTokens = EstimateTokens(string(raw))
		}

		// Link result and estimate output tokens
		if r, ok := results[toolUses[i].ToolUseID]; ok {
			toolUses[i].OutputTokens = EstimateContentTokens(r.content)
			toolUses[i].IsError = r.isError
		}
	}

	return toolUses
}

// buildDenialMap scans user messages for tool_result blocks that indicate
// the user denied a tool use. Returns a map of toolUseID → user comment.
func buildDenialMap(entries []HistoryEntry) map[string]string {
	denials := make(map[string]string)
	for _, entry := range entries {
		if entry.Message.Role != MessageRoleUser {
			continue
		}
		for _, block := range entry.Message.Content {
			if block.Type != ContentTypeToolResult || !block.IsError || block.ToolUseID == "" {
				continue
			}
			text := extractToolResultText(block)
			if !strings.HasPrefix(text, denialPrefix) {
				continue
			}
			reason := ""
			if _, after, ok := strings.Cut(text, denialCommentSeparator); ok {
				reason = strings.TrimSpace(after)
			}
			denials[block.ToolUseID] = reason
		}
	}
	return denials
}

// extractToolResultText gets the text from a tool_result content block.
// The Content field can be a JSON string or an array of content blocks.
func extractToolResultText(block ContentBlock) string {
	if block.Content == nil {
		return block.Text
	}
	// Try as plain string first
	var s string
	if err := json.Unmarshal(block.Content, &s); err == nil {
		return s
	}
	// Try as array of content blocks
	var inner []ContentBlock
	if err := json.Unmarshal(block.Content, &inner); err == nil {
		var parts []string
		for _, b := range inner {
			if b.Type == ContentTypeText && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "")
	}
	return ""
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

// RelativePath makes an absolute path relative to projectRoot if possible.
// For paths outside the project (more than 1 parent level away), returns absolute path.
func RelativePath(path, projectRoot string) string {
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

func singleLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (tu ToolUse) Interpreter() string {
	if tu.Tool != "Bash" {
		return ""
	}
	cmd, _ := tu.Input["command"].(string)
	return bash.DetectInterpreter(cmd)
}

// DisplayTool returns the display name for the tool, normalizing related tools
func (tu ToolUse) DisplayTool() string {
	switch tu.Tool {
	case "Bash":
		if interp := tu.Interpreter(); interp != "" {
			return interp
		}
		return "Bash"
	case "TaskCreate", "TodoWrite":
		return "Task"
	case "AskUserQuestion":
		return "Ask"
	default:
		return tu.Tool
	}
}

// FormatCommand extracts a human-readable command string from a ToolUse
func (tu ToolUse) FormatCommand() string {
	rel := func(path string) string {
		return RelativePath(path, tu.ProjectRoot)
	}

	switch tu.Tool {
	case "Bash":
		if cmd, ok := tu.Input["command"].(string); ok {
			if tu.ProjectRoot != "" {
				cmd = strings.ReplaceAll(cmd, tu.ProjectRoot+"/", "")
			}
			return singleLine(cmd)
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
			return singleLine(subType + ": " + desc)
		}
		if desc != "" {
			return singleLine(desc)
		}
		return subType
	case "TaskCreate":
		if subject, ok := tu.Input["subject"].(string); ok {
			return subject
		}
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
	return singleLine(string(b))
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
		return RelativePath(path, tu.ProjectRoot)
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
