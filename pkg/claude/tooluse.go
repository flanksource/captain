package claude

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/assistanttags"
	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude/tools"
	captainCollections "github.com/flanksource/captain/pkg/collections"
	"github.com/flanksource/commons/collections"
	"github.com/segmentio/encoding/json"
)

// ToolUse represents a single tool invocation extracted from history
type ToolUse struct {
	Tool            string          `json:"tool,omitempty"`
	Input           map[string]any  `json:"input,omitempty"`
	Timestamp       *time.Time      `json:"timestamp,omitempty"`
	CWD             string          `json:"cwd,omitempty"`
	SessionID       string          `json:"session_id,omitempty"`
	ToolUseID       string          `json:"tool_use_id,omitempty"`
	ProjectRoot     string          `json:"project_root,omitempty"`
	Denied          bool            `json:"denied,omitempty"`
	DeniedReason    string          `json:"deniedReason,omitempty"`
	InputTokens     int             `json:"inputTokens,omitempty"`
	OutputTokens    int             `json:"outputTokens,omitempty"`
	IsError         bool            `json:"isError,omitempty"`
	Response        string          `json:"response,omitempty"`
	Source          string          `json:"source,omitempty"` // "claude" or "codex"
	Model           string          `json:"model,omitempty"`
	ReasoningEffort string          `json:"reasoningEffort,omitempty"`
	IsSidechain     bool            `json:"isSidechain,omitempty"` // made by a nested sub-agent (Task/Agent)
	AgentID         string          `json:"agentId,omitempty"`
	AgentType       string          `json:"agentType,omitempty"` // sub-agent type, from agent-<id>.meta.json
	AgentDesc       string          `json:"agentDesc,omitempty"` // sub-agent task description, from meta.json
	RawEntry        json.RawMessage `json:"-"`
}

// Filter defines criteria for filtering tool uses
type Filter struct {
	Tools        []string
	Paths        []string
	Since        *time.Time
	Before       *time.Time
	Limit        int
	SessionID    string   // exact or prefix match against ToolUse.SessionID
	SessionIDs   []string // exact or prefix match against ToolUse.SessionID
	KeepRaw      bool     // retain raw JSONL lines for --raw output
	IncludeCosts bool     // aggregate session costs while parsing history
	// IncludeAgents, when set, makes ParseHistory also read nested sub-agent
	// transcripts (<session>/subagents/agent-*.jsonl) for the in-scope sessions.
	IncludeAgents bool
}

// MatchesSessionID reports whether sessionID satisfies the filter's SessionID
// criterion. An empty filter matches everything; otherwise an exact match or a
// prefix match (so the short IDs printed by `captain info` work) succeeds.
func (f Filter) MatchesSessionID(sessionID string) bool {
	if matchesSessionID(sessionID, f.SessionID) {
		return true
	}
	for _, filter := range f.SessionIDs {
		if matchesSessionID(sessionID, filter) {
			return true
		}
	}
	return !f.HasSessionIDFilter()
}

func (f Filter) HasSessionIDFilter() bool {
	if strings.TrimSpace(f.SessionID) != "" {
		return true
	}
	for _, filter := range f.SessionIDs {
		if strings.TrimSpace(filter) != "" {
			return true
		}
	}
	return false
}

func matchesSessionID(sessionID, filter string) bool {
	filter = strings.TrimSpace(filter)
	if filter == "" {
		return false
	}
	return sessionID == filter || strings.HasPrefix(sessionID, filter)
}

const denialPrefix = "The user doesn't want to proceed with this tool use."
const denialCommentSeparator = "the user said:\n"
const boilerplatePrefix = "\n\nNote: The user"
const askAnswerPrefix = "User has answered your question."

// ExtractToolUses extracts history activity rows from transcript entries:
// user/assistant text, reasoning, metadata events, and real tool calls.
func ExtractToolUses(entries []HistoryEntry) []ToolUse {
	var toolUses []ToolUse

	for _, entry := range entries {
		ts, _ := entry.ParseTimestamp()
		var timestamp *time.Time
		if !ts.IsZero() {
			timestamp = &ts
		}

		if isVisibleTranscriptEvent(entry.Event) {
			toolUses = append(toolUses, ToolUse{
				Tool:      tools.EventToolName(entry.Event.Type),
				Input:     eventInput(entry.Event),
				Timestamp: timestamp,
				CWD:       entry.CWD,
				SessionID: entry.SessionID,
				ToolUseID: entry.UUID,
				RawEntry:  entry.RawLine,
			})
		}

		for _, content := range entry.Message.Content {
			switch content.Type {
			case ContentTypeText:
				if entry.Message.Role == MessageRoleAssistant {
					toolUses = append(toolUses, taggedAssistantToolUses(entry, content.Text, timestamp)...)
					continue
				}
				tool := messageTool(entry.Message.Role)
				if tool == "" || content.Text == "" {
					continue
				}
				toolUses = append(toolUses, ToolUse{
					Tool:        tool,
					Input:       map[string]any{"text": content.Text},
					Timestamp:   timestamp,
					CWD:         entry.CWD,
					SessionID:   entry.SessionID,
					IsSidechain: entry.IsSidechain,
					AgentID:     entry.AgentID,
					RawEntry:    entry.RawLine,
				})
			case ContentTypeThinking, ContentTypeRedactedThinking:
				if content.Thinking == "" {
					continue
				}
				toolUses = append(toolUses, ToolUse{
					Tool:        "Reasoning",
					Input:       map[string]any{"text": content.Thinking},
					Timestamp:   timestamp,
					CWD:         entry.CWD,
					SessionID:   entry.SessionID,
					IsSidechain: entry.IsSidechain,
					AgentID:     entry.AgentID,
					RawEntry:    entry.RawLine,
				})
			case ContentTypeToolUse:
				var inputMap map[string]any
				if content.Input != nil {
					_ = json.Unmarshal(content.Input, &inputMap)
				}

				var cwd string
				if inputMap != nil {
					if v, ok := inputMap["cwd"].(string); ok {
						cwd = v
					}
				}

				toolUses = append(toolUses, ToolUse{
					Tool:        content.Name,
					Input:       inputMap,
					Timestamp:   timestamp,
					CWD:         cwd,
					SessionID:   entry.SessionID,
					ToolUseID:   content.ID,
					IsSidechain: entry.IsSidechain,
					AgentID:     entry.AgentID,
					RawEntry:    entry.RawLine,
				})
			}
		}
	}

	// Pass 2: scan user messages for denied tool_results and responses
	denials := buildDenialMap(entries)
	responses := buildResponseMap(entries)
	for i := range toolUses {
		if reason, ok := denials[toolUses[i].ToolUseID]; ok {
			toolUses[i].Denied = true
			toolUses[i].DeniedReason = reason
		}
		if resp, ok := responses[toolUses[i].ToolUseID]; ok {
			toolUses[i].Response = resp
		}
	}

	return expandUserRows(toolUses)
}

func taggedAssistantToolUses(entry HistoryEntry, text string, timestamp *time.Time) []ToolUse {
	segments := assistanttags.Parse(text)
	uses := make([]ToolUse, 0, len(segments))
	for _, segment := range segments {
		use := ToolUse{
			Timestamp:   timestamp,
			CWD:         entry.CWD,
			SessionID:   entry.SessionID,
			IsSidechain: entry.IsSidechain,
			AgentID:     entry.AgentID,
			RawEntry:    entry.RawLine,
		}
		switch segment.Kind {
		case assistanttags.SegmentPlan:
			use.Tool = "Plan"
			use.Input = map[string]any{"content": segment.Text, "tag": "proposed_plan"}
		case assistanttags.SegmentMemoryCitation:
			if segment.Citation == nil {
				continue
			}
			use.Tool = "MemoryCitation"
			use.Input = map[string]any{
				"event":            "memory_citation",
				"source":           "claude",
				"citation_entries": segment.Citation.CitationEntries,
				"rollout_ids":      segment.Citation.RolloutIDs,
			}
		case assistanttags.SegmentText:
			use.Tool = "Assistant"
			body := segment.Text
			if summary, ok := assistanttags.EnvelopeSummary(body); ok {
				body = summary
			}
			use.Input = map[string]any{"text": body}
		default:
			continue
		}
		uses = append(uses, use)
	}
	return uses
}

func isVisibleTranscriptEvent(event *TranscriptEvent) bool {
	if event == nil {
		return false
	}
	switch event.Type {
	case "file-history-snapshot", "last-prompt", "permission-mode", "agent-name":
		return false
	default:
		return true
	}
}

func messageTool(role MessageRole) string {
	switch role {
	case MessageRoleUser:
		return "User"
	case MessageRoleAssistant:
		return "Assistant"
	default:
		return ""
	}
}

func eventInput(event *TranscriptEvent) map[string]any {
	input := map[string]any{"event": event.Type}
	if event.Scope != "" {
		input["scope"] = event.Scope
	}
	if event.Subtype != "" {
		input["subtype"] = event.Subtype
	}
	for k, v := range event.Data {
		input[k] = v
	}
	return input
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
		if raw, err := json.Marshal(toolUses[i].Input); err == nil {
			toolUses[i].InputTokens = EstimateTokens(string(raw))
		}
		if r, ok := results[toolUses[i].ToolUseID]; ok {
			toolUses[i].OutputTokens = EstimateContentTokens(r.content)
			toolUses[i].IsError = r.isError
			toolUses[i].Response = extractResultText(r.content)
		}
	}

	return toolUses
}

// ExtractTools returns Tool interface implementations from history entries.
func ExtractTools(entries []HistoryEntry) []tools.Tool {
	return toTools(ExtractToolUses(entries), entries)
}

// ExtractToolsWithTokens returns Tool interface implementations with token estimates.
func ExtractToolsWithTokens(entries []HistoryEntry) []tools.Tool {
	return toTools(ExtractToolUsesWithTokens(entries), entries)
}

func toTools(toolUses []ToolUse, entries []HistoryEntry) []tools.Tool {
	// Build map of toolUseID → (model, usage) from assistant messages
	type msgInfo struct {
		model string
		usage *Usage
	}
	toolMsg := make(map[string]msgInfo)
	for _, entry := range entries {
		if entry.Message.Role != MessageRoleAssistant {
			continue
		}
		for _, block := range entry.Message.Content {
			if block.Type == ContentTypeToolUse && block.ID != "" {
				toolMsg[block.ID] = msgInfo{
					model: entry.Message.Model,
					usage: entry.Message.Usage,
				}
			}
		}
	}

	result := make([]tools.Tool, 0, len(toolUses))
	for _, tu := range toolUses {
		base := tools.BaseTool{
			RawTool:      tu.Tool,
			Input:        tu.Input,
			Timestamp:    tu.Timestamp,
			CWD:          tu.CWD,
			SessionID:    tu.SessionID,
			ToolUseID:    tu.ToolUseID,
			ProjectRoot:  tu.ProjectRoot,
			Denied:       tu.Denied,
			DeniedReason: tu.DeniedReason,
			IsError:      tu.IsError,
			Response:     tu.Response,
			IsSidechain:  tu.IsSidechain,
			AgentID:      tu.AgentID,
			AgentType:    tu.AgentType,
			AgentDesc:    tu.AgentDesc,
			RawEntry:     tu.RawEntry,
		}

		if info, ok := toolMsg[tu.ToolUseID]; ok && info.usage != nil {
			mu := tools.ModelUsage{
				Model:                    info.model,
				InputTokens:              info.usage.InputTokens,
				OutputTokens:             info.usage.OutputTokens,
				CacheCreationInputTokens: info.usage.CacheCreationInputTokens,
				CacheReadInputTokens:     info.usage.CacheReadInputTokens,
				ServiceTier:              info.usage.ServiceTier,
				Cost:                     CalculateCost(info.usage, info.model),
			}
			base.Models = tools.Models{mu}
		} else if tu.InputTokens > 0 || tu.OutputTokens > 0 {
			base.Models = tools.Models{{
				InputTokens:  tu.InputTokens,
				OutputTokens: tu.OutputTokens,
			}}
		}

		result = append(result, tools.NewTool(base))
	}
	return result
}

// ToolUsesToTools converts a slice of ToolUse to Tool interfaces.
func ToolUsesToTools(toolUses []ToolUse) []tools.Tool {
	result := make([]tools.Tool, 0, len(toolUses))
	for _, tu := range toolUses {
		base := tools.BaseTool{
			RawTool:         tu.Tool,
			Input:           tu.Input,
			Timestamp:       tu.Timestamp,
			CWD:             tu.CWD,
			SessionID:       tu.SessionID,
			ToolUseID:       tu.ToolUseID,
			ProjectRoot:     tu.ProjectRoot,
			Denied:          tu.Denied,
			DeniedReason:    tu.DeniedReason,
			IsError:         tu.IsError,
			Response:        tu.Response,
			Source:          tu.Source,
			ReasoningEffort: tu.ReasoningEffort,
			IsSidechain:     tu.IsSidechain,
			AgentID:         tu.AgentID,
			AgentType:       tu.AgentType,
			AgentDesc:       tu.AgentDesc,
			RawEntry:        tu.RawEntry,
		}
		if tu.Model != "" || tu.InputTokens > 0 || tu.OutputTokens > 0 {
			base.Models = tools.Models{{
				Model:        tu.Model,
				InputTokens:  tu.InputTokens,
				OutputTokens: tu.OutputTokens,
			}}
		}
		result = append(result, tools.NewTool(base))
	}
	return result
}

// expandUserRows inserts synthetic "User" rows after tool uses that have
// user responses (denied tools, AskUserQuestion answers).
func expandUserRows(toolUses []ToolUse) []ToolUse {
	var result []ToolUse
	for _, tu := range toolUses {
		// Merge ExitPlanMode into the preceding Plan write row
		if tu.Tool == "ExitPlanMode" {
			if tu.Denied {
				// Set Denied on the previous Plan row if it exists
				for i := len(result) - 1; i >= 0; i-- {
					if isPlanFileToolUse(result[i]) {
						result[i].Denied = true
						result[i].DeniedReason = tu.DeniedReason
						break
					}
				}
				// Still create User row for the denial comment
				if tu.DeniedReason != "" {
					result = append(result, ToolUse{
						Tool:      "User",
						Input:     map[string]any{"text": tu.DeniedReason},
						Timestamp: tu.Timestamp,
						SessionID: tu.SessionID,
					})
				}
			}
			// Skip the ExitPlanMode row itself (approved or denied)
			continue
		}

		result = append(result, tu)

		var userText string
		switch {
		case tu.Denied && tu.DeniedReason != "":
			userText = tu.DeniedReason
		case tu.Tool == "AskUserQuestion" && tu.Response != "":
			userText = tu.Response
		}
		if userText == "" {
			continue
		}
		result = append(result, ToolUse{
			Tool:      "User",
			Input:     map[string]any{"text": userText},
			Timestamp: tu.Timestamp,
			SessionID: tu.SessionID,
		})
	}
	return result
}

func isPlanFileToolUse(tu ToolUse) bool {
	if tu.Tool != "Write" && tu.Tool != "Edit" && tu.Tool != "Read" {
		return false
	}
	if fp, ok := tu.Input["file_path"].(string); ok {
		return strings.Contains(fp, "/.claude/plans/")
	}
	return false
}

func extractResultText(content json.RawMessage) string {
	if len(content) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(content, &s) == nil {
		return s
	}
	var blocks []ContentBlock
	if json.Unmarshal(content, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Type == ContentTypeText && b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, "")
	}
	return ""
}

func stripBoilerplate(s string) string {
	if i := strings.Index(s, boilerplatePrefix); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if strings.HasPrefix(s, askAnswerPrefix) {
		s = strings.TrimSpace(strings.TrimPrefix(s, askAnswerPrefix))
	}
	return s
}

// buildResponseMap extracts non-denial tool_result text for tools that need it.
func buildResponseMap(entries []HistoryEntry) map[string]string {
	responses := make(map[string]string)
	for _, entry := range entries {
		if entry.Message.Role != MessageRoleUser {
			continue
		}
		for _, block := range entry.Message.Content {
			if block.Type != ContentTypeToolResult || block.ToolUseID == "" || block.IsError {
				continue
			}
			text := stripBoilerplate(extractToolResultText(block))
			if text != "" {
				responses[block.ToolUseID] = text
			}
		}
	}
	return responses
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
				reason = stripBoilerplate(strings.TrimSpace(after))
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

// FilterToolUses applies filter criteria to tool uses.
// When filters produce no results, suggestions are printed to stderr.
func FilterToolUses(toolUses []ToolUse, filter Filter) []ToolUse {
	var filtered []ToolUse
	toolNames := make(map[string]struct{})
	dirs := make(map[string]struct{})

	for _, tu := range toolUses {
		toolNames[tu.Tool] = struct{}{}

		if !filter.MatchesSessionID(tu.SessionID) {
			continue
		}

		if len(filter.Tools) > 0 && !collections.MatchItems(tu.Tool, filter.Tools...) {
			continue
		}

		if len(filter.Paths) > 0 {
			fp := tu.FilePath()
			dir := tu.CWD
			if fp != "" {
				dir = filepath.Dir(fp)
			}
			if dir != "" {
				dirs[dir] = struct{}{}
				matched := collections.MatchItems(dir, filter.Paths...) ||
					(fp != "" && collections.MatchItems(fp, filter.Paths...))
				if !matched {
					continue
				}
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

	if len(filtered) == 0 {
		suggestFilters("tool", filter.Tools, toolNames)
		suggestFilters("path", filter.Paths, dirs)
	}

	return filtered
}

func suggestFilters(filterName string, filters []string, available map[string]struct{}) {
	if len(filters) == 0 || len(available) == 0 {
		return
	}
	candidates := make([]string, 0, len(available))
	for v := range available {
		candidates = append(candidates, v)
	}
	for _, f := range filters {
		if similar := captainCollections.FindSimilar(f, candidates, 3); len(similar) > 0 {
			fmt.Fprintf(os.Stderr, "%s filter %q matched nothing. Did you mean: %s?\n", filterName, f, strings.Join(similar, ", "))
		}
	}
}

// AbsolutePath anchors a tool-use path to an absolute, cleaned path.
//
// Tool inputs mix absolute paths (Read/Write/Edit carry them) with paths
// relative to the directory the tool ran in (a bash `cat pkg/x.go`), and an
// agent's working directory moves during a session — so a path is only
// unambiguous once anchored to the cwd that produced it. Anchoring prefers that
// cwd and falls back to the project root.
//
// This is the canonical form every consumer should hold: relativising per tool
// use bakes in whichever base that call happened to have, which silently differs
// across one session and leaves the path meaningless to anyone else. Render with
// RelativePath at the point of display instead.
//
// A path that cannot be anchored — no base, or a fragment the shell never
// expanded, e.g. "$DIR/x" — is returned cleaned but otherwise unchanged rather
// than guessed at.
func AbsolutePath(path, cwd, projectRoot string) string {
	if path == "" {
		return path
	}
	anchored := func() string {
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		base := cwd
		if base == "" {
			base = projectRoot
		}
		if base == "" {
			return filepath.Clean(path)
		}
		return filepath.Join(base, path)
	}()
	// Clean and Join drop a trailing separator, but that separator is how a
	// directory argument (Grep/Glob `pkg/`) is told apart from a file, so keep it.
	if strings.HasSuffix(path, "/") && !strings.HasSuffix(anchored, "/") {
		anchored += "/"
	}
	return anchored
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

func (tu ToolUse) firstQuestion() string {
	if q, ok := tu.Input["question"].(string); ok {
		return q
	}
	if questions, ok := tu.Input["questions"].([]any); ok && len(questions) > 0 {
		if q, ok := questions[0].(map[string]any); ok {
			if text, ok := q["question"].(string); ok {
				return text
			}
		}
		if q, ok := questions[0].(string); ok {
			return q
		}
	}
	return ""
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
	if tu.isPlanFile() {
		return "Plan"
	}
	switch tu.Tool {
	case "Bash":
		if interp := tu.Interpreter(); interp != "" {
			return interp
		}
		return "Bash"
	case "Agent":
		return "Agent"
	case "ExitPlanMode":
		return "Plan"
	case "TaskCreate", "TodoWrite":
		return "Task"
	case "AskUserQuestion":
		return "Ask"
	default:
		return tu.Tool
	}
}

func (tu ToolUse) isPlanFile() bool {
	if tu.Tool != "Write" && tu.Tool != "Edit" && tu.Tool != "Read" {
		return false
	}
	return strings.Contains(tu.FilePath(), "/.claude/plans/")
}

// FormatCommand extracts a human-readable command string from a ToolUse
func (tu ToolUse) FormatCommand() string {
	if tu.isPlanFile() {
		name := filepath.Base(tu.FilePath())
		return strings.TrimSuffix(name, ".md")
	}

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
		if q := tu.firstQuestion(); q != "" {
			return q
		}
	case "ExitPlanMode":
		if tu.Denied {
			return "✗ disapproved"
		}
		return "✓ approved"
	case "Agent", "Task":
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
	case "User":
		if text, ok := tu.Input["text"].(string); ok {
			return text
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
