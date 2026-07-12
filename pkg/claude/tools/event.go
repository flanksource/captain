package tools

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

// EventToolName maps raw Codex/Claude event names to concrete synthetic tools.
// The raw event name is still preserved in BaseTool.Input["event"].
func EventToolName(eventType string) string {
	switch strings.TrimSpace(eventType) {
	case "token_count":
		return "TokenCount"
	case "task_started":
		return "TaskStarted"
	case "task_complete":
		return "TaskComplete"
	case "turn_aborted":
		return "TurnAborted"
	case "context_compacted":
		return "ContextCompacted"
	case "thread_rolled_back":
		return "ThreadRolledBack"
	case "item_completed":
		return "ItemCompleted"
	case "exec_command_end":
		return "CodexExecCommand"
	case "patch_apply_end":
		return "CodexPatchApply"
	case "mcp_tool_call_end":
		return "MCPToolCall"
	case "web_search_end":
		return "WebSearchEvent"
	case "view_image_tool_call":
		return "ViewImage"
	case "guardian_assessment":
		return "GuardianAssessment"
	case "entered_review_mode", "exited_review_mode":
		return "ReviewMode"
	case "collab_agent_spawn_end":
		return "CollabAgentSpawn"
	case "collab_agent_interaction_end":
		return "CollabAgentInteraction"
	case "collab_waiting_end":
		return "CollabWaiting"
	case "collab_close_end":
		return "CollabClose"
	case "error":
		return "ApiError"
	case "queue-operation":
		return "QueueOperation"
	case "deferred_tools_delta":
		return "DeferredToolsDelta"
	case "agent_listing_delta":
		return "AgentListingDelta"
	case "memory_citation":
		return "MemoryCitation"
	case "skill_listing":
		return "SkillListing"
	case "budget_usd":
		return "Budget"
	case "worktree-state":
		return "WorktreeState"
	case "relocated":
		return "Relocated"
	case "started":
		return "Started"
	default:
		return "Event"
	}
}

// IsEventToolName reports whether name is a synthetic transcript event row.
func IsEventToolName(name string) bool {
	switch name {
	case "Event", "TokenCount", "TaskStarted", "TaskComplete", "TurnAborted",
		"ContextCompacted", "ThreadRolledBack", "ItemCompleted",
		"CodexExecCommand", "CodexPatchApply", "MCPToolCall",
		"WebSearchEvent", "ViewImage", "GuardianAssessment", "ReviewMode",
		"CollabAgentSpawn", "CollabAgentInteraction", "CollabWaiting",
		"CollabClose", "QueueOperation", "DeferredToolsDelta",
		"AgentListingDelta", "MemoryCitation", "SkillListing", "Budget", "PrLink",
		"CompactBoundary", "LocalCommand", "ScheduledTaskFire",
		"Informational", "WorktreeState", "Relocated", "Started":
		return true
	default:
		return false
	}
}

type EventTool struct{ BaseTool }

func (t *EventTool) Name() string        { return "Event" }
func (t *EventTool) Category() string    { return "chat" }
func (t *EventTool) FilePath() string    { return "" }
func (t *EventTool) ExtractPath() string { return "" }

func (t *EventTool) Pretty() api.Text {
	name := t.Str("event")
	if name == "" {
		name = "event"
	}
	text := clicky.Text("").
		Add(icons.Info).
		Append(" "+name, "text-slate-500 font-medium")
	if msg := t.Str("message"); msg != "" {
		text = text.Append(" "+msg, "text-gray-500 max-w-[tw-20ch]")
	}
	if duration := eventInt(t.Input["duration_ms"]); duration > 0 {
		text = text.Append(fmt.Sprintf(" %dms", duration), "text-gray-500")
	}
	return text
}

func (t *EventTool) Detail() api.Textable { return t.BaseTool.Detail() }

type TokenCountTool struct{ BaseTool }

func (t *TokenCountTool) Name() string         { return "TokenCount" }
func (t *TokenCountTool) Category() string     { return "chat" }
func (t *TokenCountTool) FilePath() string     { return "" }
func (t *TokenCountTool) ExtractPath() string  { return "" }
func (t *TokenCountTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *TokenCountTool) Pretty() api.Text {
	text := eventText(icons.Icon{Unicode: "◌", Iconify: "mdi:counter", Style: "muted"}, "tokens", "text-slate-500 font-medium")
	if total := eventInt(t.Input["total_tokens"]); total > 0 {
		text = text.Append(" "+FormatCompactTokens(int(total)), "text-gray-600")
	}
	if in := eventInt(t.Input["input_tokens"]); in > 0 {
		text = text.Append(fmt.Sprintf(" in=%s", FormatCompactTokens(int(in))), "text-gray-500")
	}
	if out := eventInt(t.Input["output_tokens"]); out > 0 {
		text = text.Append(fmt.Sprintf(" out=%s", FormatCompactTokens(int(out))), "text-gray-500")
	}
	return text
}

type TaskStartedTool struct{ BaseTool }

func (t *TaskStartedTool) Name() string         { return "TaskStarted" }
func (t *TaskStartedTool) Category() string     { return "chat" }
func (t *TaskStartedTool) FilePath() string     { return "" }
func (t *TaskStartedTool) ExtractPath() string  { return "" }
func (t *TaskStartedTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *TaskStartedTool) Pretty() api.Text {
	text := eventText(icons.Play, "task started", "text-green-600 font-medium")
	if mode := t.Str("collaboration_mode_kind"); mode != "" {
		text = text.Append(" "+mode, "text-gray-500")
	}
	if window := eventInt(t.Input["model_context_window"]); window > 0 {
		text = text.Append(fmt.Sprintf(" ctx=%s", FormatCompactTokens(int(window))), "text-gray-500")
	}
	return text
}

type TaskCompleteTool struct{ BaseTool }

func (t *TaskCompleteTool) Name() string         { return "TaskComplete" }
func (t *TaskCompleteTool) Category() string     { return "chat" }
func (t *TaskCompleteTool) FilePath() string     { return "" }
func (t *TaskCompleteTool) ExtractPath() string  { return "" }
func (t *TaskCompleteTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *TaskCompleteTool) Pretty() api.Text {
	text := eventText(icons.Check, "task complete", "text-green-600 font-medium")
	if duration := eventInt(t.Input["duration_ms"]); duration > 0 {
		text = text.Append(" "+formatDurationMS(float64(duration)), "text-gray-500")
	}
	if msg := eventString(t.Input["last_agent_message"]); msg != "" {
		text = text.Append(" "+eventPreview(msg, 90), "text-gray-500 italic")
	}
	return text
}

type LifecycleEventTool struct{ BaseTool }

func (t *LifecycleEventTool) Name() string         { return t.RawTool }
func (t *LifecycleEventTool) Category() string     { return "chat" }
func (t *LifecycleEventTool) FilePath() string     { return "" }
func (t *LifecycleEventTool) ExtractPath() string  { return "" }
func (t *LifecycleEventTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *LifecycleEventTool) Pretty() api.Text {
	label := strings.TrimSpace(t.Str("event"))
	if label == "" {
		label = strings.ToLower(t.RawTool)
	}
	text := eventText(icons.Info, strings.ReplaceAll(label, "_", " "), "text-slate-500 font-medium")
	if reason := t.Str("reason"); reason != "" {
		text = text.Append(" "+reason, "text-gray-500")
	}
	if turns := eventInt(t.Input["num_turns"]); turns > 0 {
		text = text.Append(fmt.Sprintf(" turns=%d", turns), "text-gray-500")
	}
	return text
}

type CodexExecCommandTool struct{ BaseTool }

func (t *CodexExecCommandTool) Name() string     { return "CodexExecCommand" }
func (t *CodexExecCommandTool) Category() string { return "chat" }
func (t *CodexExecCommandTool) FilePath() string { return "" }
func (t *CodexExecCommandTool) ExtractPath() string {
	if cwd := t.Str("cwd"); cwd != "" {
		return cwd
	}
	return ""
}
func (t *CodexExecCommandTool) Detail() api.Textable { return commandOutputDetail(t.BaseTool) }
func (t *CodexExecCommandTool) Pretty() api.Text {
	text := eventText(icons.Icon{Unicode: "💻", Iconify: "codicon:terminal", Style: "muted"}, "exec", "text-green-500 font-medium")
	if cmd := codexCommandString(t.Input["command"]); cmd != "" {
		text = text.Append(" "+eventPreview(cmd, 120), "text-gray-700")
	}
	if status := t.Str("status"); status != "" {
		color := "text-green-600"
		if status != "completed" && status != "success" {
			color = "text-red-500"
		}
		text = text.Append(" "+status, color)
	}
	if code := eventInt(t.Input["exit_code"]); code != 0 {
		text = text.Append(fmt.Sprintf(" exit=%d", code), "text-red-500")
	}
	return text
}

type CodexPatchApplyTool struct{ BaseTool }

func (t *CodexPatchApplyTool) Name() string         { return "CodexPatchApply" }
func (t *CodexPatchApplyTool) Category() string     { return "chat" }
func (t *CodexPatchApplyTool) FilePath() string     { return "" }
func (t *CodexPatchApplyTool) ExtractPath() string  { return "" }
func (t *CodexPatchApplyTool) Detail() api.Textable { return commandOutputDetail(t.BaseTool) }
func (t *CodexPatchApplyTool) Pretty() api.Text {
	text := eventText(icons.Icon{Unicode: "✏️", Iconify: "codicon:edit", Style: "muted"}, "patch", "text-orange-500 font-medium")
	if success, ok := t.Input["success"].(bool); ok {
		if success {
			text = text.Append(" applied", "text-green-600")
		} else {
			text = text.Append(" failed", "text-red-500")
		}
	}
	if n := mapLen(t.Input["changes"]); n > 0 {
		text = text.Append(fmt.Sprintf(" files=%d", n), "text-gray-500")
	}
	return text
}

type MCPToolCallTool struct{ BaseTool }

func (t *MCPToolCallTool) Name() string         { return "MCPToolCall" }
func (t *MCPToolCallTool) Category() string     { return "chat" }
func (t *MCPToolCallTool) FilePath() string     { return "" }
func (t *MCPToolCallTool) ExtractPath() string  { return "" }
func (t *MCPToolCallTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *MCPToolCallTool) Pretty() api.Text {
	server, tool := invocationName(t.Input["invocation"])
	text := eventText(icons.Package, "mcp", "text-indigo-500 font-medium")
	if server != "" || tool != "" {
		text = text.Append(" "+strings.Trim(server+"."+tool, "."), "text-gray-700")
	}
	if d := durationString(t.Input["duration"]); d != "" {
		text = text.Append(" "+d, "text-gray-500")
	}
	if resultStatus := resultStatus(t.Input["result"]); resultStatus != "" {
		text = text.Append(" "+resultStatus, statusColor(resultStatus))
	}
	return text
}

type WebSearchEventTool struct{ BaseTool }

func (t *WebSearchEventTool) Name() string         { return "WebSearchEvent" }
func (t *WebSearchEventTool) Category() string     { return "chat" }
func (t *WebSearchEventTool) FilePath() string     { return "" }
func (t *WebSearchEventTool) ExtractPath() string  { return "" }
func (t *WebSearchEventTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *WebSearchEventTool) Pretty() api.Text {
	text := eventText(icons.Search, "web search", "text-purple-500 font-medium")
	if query := firstNonEmptyEvent(t.Str("query"), actionQuery(t.Input["action"])); query != "" {
		text = text.Append(" "+eventPreview(query, 100), "text-gray-700")
	}
	return text
}

type ViewImageTool struct{ BaseTool }

func (t *ViewImageTool) Name() string         { return "ViewImage" }
func (t *ViewImageTool) Category() string     { return "chat" }
func (t *ViewImageTool) FilePath() string     { return t.Str("path") }
func (t *ViewImageTool) ExtractPath() string  { return t.Str("path") }
func (t *ViewImageTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *ViewImageTool) Pretty() api.Text {
	text := eventText(icons.File, "image", "text-cyan-500 font-medium")
	if path := t.Str("path"); path != "" {
		text = text.Append(" "+ShortenPath(path), "text-gray-700")
	}
	return text
}

type GuardianAssessmentTool struct{ BaseTool }

func (t *GuardianAssessmentTool) Name() string         { return "GuardianAssessment" }
func (t *GuardianAssessmentTool) Category() string     { return "chat" }
func (t *GuardianAssessmentTool) FilePath() string     { return "" }
func (t *GuardianAssessmentTool) ExtractPath() string  { return "" }
func (t *GuardianAssessmentTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *GuardianAssessmentTool) Pretty() api.Text {
	text := eventText(icons.Icon{Unicode: "🛡", Iconify: "mdi:shield-check", Style: "muted"}, "guardian", "text-amber-600 font-medium")
	if status := t.Str("status"); status != "" {
		text = text.Append(" "+status, statusColor(status))
	}
	if action := actionSummary(t.Input["action"]); action != "" {
		text = text.Append(" "+eventPreview(action, 100), "text-gray-600")
	}
	return text
}

type ReviewModeTool struct{ BaseTool }

func (t *ReviewModeTool) Name() string         { return "ReviewMode" }
func (t *ReviewModeTool) Category() string     { return "chat" }
func (t *ReviewModeTool) FilePath() string     { return "" }
func (t *ReviewModeTool) ExtractPath() string  { return "" }
func (t *ReviewModeTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *ReviewModeTool) Pretty() api.Text {
	action := strings.TrimSuffix(strings.TrimPrefix(t.Str("event"), "entered_"), "_mode")
	if action == "" {
		action = strings.TrimSuffix(strings.TrimPrefix(t.Str("event"), "exited_"), "_mode")
	}
	text := eventText(icons.Info, "review "+action, "text-purple-500 font-medium")
	if n := findingsCount(t.Input["review_output"]); n > 0 {
		text = text.Append(fmt.Sprintf(" findings=%d", n), "text-gray-500")
	}
	return text
}

type CollabEventTool struct{ BaseTool }

func (t *CollabEventTool) Name() string         { return t.RawTool }
func (t *CollabEventTool) Category() string     { return "chat" }
func (t *CollabEventTool) FilePath() string     { return "" }
func (t *CollabEventTool) ExtractPath() string  { return "" }
func (t *CollabEventTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *CollabEventTool) Pretty() api.Text {
	label := strings.TrimPrefix(strings.TrimPrefix(t.Str("event"), "collab_"), "agent_")
	label = strings.TrimSuffix(label, "_end")
	if label == "" {
		label = strings.ToLower(t.RawTool)
	}
	text := eventText(icons.Icon{Unicode: "🤝", Iconify: "mdi:handshake", Style: "muted"}, "collab "+strings.ReplaceAll(label, "_", " "), "text-indigo-500 font-medium")
	if nick := firstNonEmptyEvent(t.Str("nickname"), nestedString(t.Input["receiver"], "nickname")); nick != "" {
		text = text.Append(" "+nick, "text-gray-700")
	}
	if status := t.Str("status"); status != "" {
		text = text.Append(" "+status, statusColor(status))
	}
	return text
}

type QueueOperationTool struct{ BaseTool }

func (t *QueueOperationTool) Name() string         { return "QueueOperation" }
func (t *QueueOperationTool) Category() string     { return "chat" }
func (t *QueueOperationTool) FilePath() string     { return "" }
func (t *QueueOperationTool) ExtractPath() string  { return "" }
func (t *QueueOperationTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *QueueOperationTool) Pretty() api.Text {
	op := firstNonEmptyEvent(t.Str("operation"), "queue")
	text := eventText(icons.Package, "queue "+op, "text-slate-500 font-medium")
	if content := t.Str("content"); content != "" {
		text = text.Append(" "+eventPreview(content, 100), "text-gray-500")
	}
	return text
}

type DeferredToolsDeltaTool struct{ BaseTool }

func (t *DeferredToolsDeltaTool) Name() string         { return "DeferredToolsDelta" }
func (t *DeferredToolsDeltaTool) Category() string     { return "chat" }
func (t *DeferredToolsDeltaTool) FilePath() string     { return "" }
func (t *DeferredToolsDeltaTool) ExtractPath() string  { return "" }
func (t *DeferredToolsDeltaTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *DeferredToolsDeltaTool) Pretty() api.Text {
	text := eventText(icons.Package, "tools", "text-slate-500 font-medium")
	appendListCount(&text, "added", t.Input["addedNames"])
	appendListCount(&text, "pending", t.Input["pendingMcpServers"])
	return text
}

type AgentListingDeltaTool struct{ BaseTool }

func (t *AgentListingDeltaTool) Name() string         { return "AgentListingDelta" }
func (t *AgentListingDeltaTool) Category() string     { return "chat" }
func (t *AgentListingDeltaTool) FilePath() string     { return "" }
func (t *AgentListingDeltaTool) ExtractPath() string  { return "" }
func (t *AgentListingDeltaTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *AgentListingDeltaTool) Pretty() api.Text {
	text := eventText(icons.Icon{Unicode: "🤖", Iconify: "mdi:robot", Style: "muted"}, "agents", "text-indigo-500 font-medium")
	appendListCount(&text, "added", t.Input["addedTypes"])
	return text
}

type SkillListingTool struct{ BaseTool }

func (t *SkillListingTool) Name() string         { return "SkillListing" }
func (t *SkillListingTool) Category() string     { return "chat" }
func (t *SkillListingTool) FilePath() string     { return "" }
func (t *SkillListingTool) ExtractPath() string  { return "" }
func (t *SkillListingTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *SkillListingTool) Pretty() api.Text {
	text := eventText(icons.Info, "skills", "text-teal-500 font-medium")
	appendListCount(&text, "count", t.Input["names"])
	if count := eventInt(t.Input["skillCount"]); count > 0 {
		text = text.Append(fmt.Sprintf(" count=%d", count), "text-gray-500")
	}
	return text
}

type BudgetTool struct{ BaseTool }

func (t *BudgetTool) Name() string         { return "Budget" }
func (t *BudgetTool) Category() string     { return "chat" }
func (t *BudgetTool) FilePath() string     { return "" }
func (t *BudgetTool) ExtractPath() string  { return "" }
func (t *BudgetTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *BudgetTool) Pretty() api.Text {
	text := eventText(icons.Icon{Unicode: "$", Iconify: "mdi:cash", Style: "muted"}, "budget", "text-green-600 font-medium")
	if used := eventFloat(t.Input["used"]); used > 0 {
		text = text.Append(fmt.Sprintf(" used=$%.2f", used), "text-gray-600")
	}
	if total := eventFloat(t.Input["total"]); total > 0 {
		text = text.Append(fmt.Sprintf(" total=$%.2f", total), "text-gray-500")
	}
	if remaining := eventFloat(t.Input["remaining"]); remaining > 0 {
		text = text.Append(fmt.Sprintf(" remaining=$%.2f", remaining), "text-gray-500")
	}
	return text
}

type PrLinkTool struct{ BaseTool }

func (t *PrLinkTool) Name() string         { return "PrLink" }
func (t *PrLinkTool) Category() string     { return "chat" }
func (t *PrLinkTool) FilePath() string     { return "" }
func (t *PrLinkTool) ExtractPath() string  { return "" }
func (t *PrLinkTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *PrLinkTool) Pretty() api.Text {
	text := eventText(icons.Git, "pr", "text-cyan-600 font-medium")
	if n := eventInt(t.Input["prNumber"]); n > 0 {
		text = text.Append(fmt.Sprintf(" #%d", n), "text-gray-700")
	}
	if repo := t.Str("prRepository"); repo != "" {
		text = text.Append(" "+repo, "text-gray-500")
	}
	return text
}

type ContentEventTool struct{ BaseTool }

func (t *ContentEventTool) Name() string        { return t.RawTool }
func (t *ContentEventTool) Category() string    { return "chat" }
func (t *ContentEventTool) FilePath() string    { return "" }
func (t *ContentEventTool) ExtractPath() string { return "" }
func (t *ContentEventTool) Detail() api.Textable {
	if d := t.BaseTool.Detail(); d != nil {
		return d
	}
	if c := strings.TrimSpace(t.Str("content")); c != "" {
		text := clicky.Text("").Append(c, "")
		return &text
	}
	return nil
}
func (t *ContentEventTool) Pretty() api.Text {
	label := strings.TrimSpace(t.Str("event"))
	if label == "" {
		label = strings.ToLower(t.RawTool)
	}
	text := eventText(icons.Info, strings.ReplaceAll(label, "_", " "), "text-slate-500 font-medium")
	if content := t.Str("content"); content != "" {
		text = text.Append(" "+eventPreview(content, 100), "text-gray-500")
	}
	return text
}

type WorktreeStateTool struct{ BaseTool }

func (t *WorktreeStateTool) Name() string     { return "WorktreeState" }
func (t *WorktreeStateTool) Category() string { return "chat" }
func (t *WorktreeStateTool) FilePath() string {
	return nestedString(t.Input["worktreeSession"], "worktreePath")
}
func (t *WorktreeStateTool) ExtractPath() string  { return t.FilePath() }
func (t *WorktreeStateTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *WorktreeStateTool) Pretty() api.Text {
	text := eventText(icons.Git, "worktree", "text-green-600 font-medium")
	if name := nestedString(t.Input["worktreeSession"], "worktreeName"); name != "" {
		text = text.Append(" "+name, "text-gray-700")
	}
	if branch := nestedString(t.Input["worktreeSession"], "worktreeBranch"); branch != "" {
		text = text.Append(" "+branch, "text-gray-500")
	}
	return text
}

type RelocatedTool struct{ BaseTool }

func (t *RelocatedTool) Name() string         { return "Relocated" }
func (t *RelocatedTool) Category() string     { return "chat" }
func (t *RelocatedTool) FilePath() string     { return t.Str("relocatedCwd") }
func (t *RelocatedTool) ExtractPath() string  { return t.Str("relocatedCwd") }
func (t *RelocatedTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *RelocatedTool) Pretty() api.Text {
	text := eventText(icons.Folder, "relocated", "text-cyan-600 font-medium")
	if cwd := t.Str("relocatedCwd"); cwd != "" {
		text = text.Append(" "+ShortenPath(cwd), "text-gray-700")
	}
	return text
}

type StartedTool struct{ BaseTool }

func (t *StartedTool) Name() string         { return "Started" }
func (t *StartedTool) Category() string     { return "chat" }
func (t *StartedTool) FilePath() string     { return "" }
func (t *StartedTool) ExtractPath() string  { return "" }
func (t *StartedTool) Detail() api.Textable { return t.BaseTool.Detail() }
func (t *StartedTool) Pretty() api.Text {
	return eventText(icons.Play, "started", "text-green-600 font-medium")
}

func eventInt(value any) int64 {
	switch v := value.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	default:
		return 0
	}
}

func eventFloat(value any) float64 {
	switch v := value.(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	default:
		return 0
	}
}

func eventString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		return ""
	}
}

func eventText(icon api.Textable, label, color string) api.Text {
	return clicky.Text("").Add(icon).Append(" "+label, color)
}

func eventPreview(s string, max int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func firstNonEmptyEvent(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func mapLen(value any) int {
	switch v := value.(type) {
	case map[string]any:
		return len(v)
	case map[string]map[string]any:
		return len(v)
	default:
		return 0
	}
}

func listLen(value any) int {
	switch v := value.(type) {
	case []any:
		return len(v)
	case []string:
		return len(v)
	default:
		return 0
	}
}

func appendListCount(text *api.Text, label string, value any) {
	if n := listLen(value); n > 0 {
		*text = text.Append(fmt.Sprintf(" %s=%d", label, n), "text-gray-500")
	}
}

func statusColor(status string) string {
	switch strings.ToLower(status) {
	case "completed", "success", "ok", "approved", "pass", "passed":
		return "text-green-600"
	case "failed", "error", "denied", "blocked", "rejected", "interrupted":
		return "text-red-500"
	default:
		return "text-gray-500"
	}
}

func codexCommandString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, " ")
	case []string:
		return strings.Join(v, " ")
	default:
		return ""
	}
}

func commandOutputDetail(base BaseTool) api.Textable {
	if d := base.Detail(); d != nil {
		return d
	}
	stdout := strings.TrimSpace(base.Str("stdout"))
	stderr := strings.TrimSpace(base.Str("stderr"))
	if stdout == "" && stderr == "" {
		stdout = strings.TrimSpace(base.Str("aggregated_output"))
	}
	if stdout == "" && stderr == "" {
		return nil
	}
	text := clicky.Text("")
	if stdout != "" {
		text = text.Append("stdout: ", "font-bold text-gray-600").Append(stdout, "")
	}
	if stderr != "" {
		if stdout != "" {
			text = text.NewLine()
		}
		text = text.Append("stderr: ", "font-bold text-red-500").Append(stderr, "")
	}
	return &text
}

func invocationName(value any) (string, string) {
	m, ok := value.(map[string]any)
	if !ok {
		return "", ""
	}
	return eventString(m["server"]), eventString(m["tool"])
}

func durationString(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if secs := eventFloat(m["secs"]); secs > 0 {
		return formatDurationMS(secs * 1000)
	}
	if nanos := eventFloat(m["nanos"]); nanos > 0 {
		return formatDurationMS(nanos / 1_000_000)
	}
	return ""
}

func resultStatus(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	for k := range m {
		if strings.EqualFold(k, "ok") {
			return "ok"
		}
		if strings.EqualFold(k, "err") || strings.EqualFold(k, "error") {
			return "error"
		}
	}
	return ""
}

func actionQuery(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	if q := eventString(m["query"]); q != "" {
		return q
	}
	if qs, ok := m["queries"].([]any); ok && len(qs) > 0 {
		if q, ok := qs[0].(string); ok {
			return q
		}
	}
	return ""
}

func actionSummary(value any) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	parts := make([]string, 0, 3)
	for _, key := range []string{"tool", "command", "cwd"} {
		if value := eventString(m[key]); value != "" {
			if key == "cwd" {
				value = filepath.Base(value)
			}
			parts = append(parts, value)
		}
	}
	return strings.Join(parts, " ")
}

func findingsCount(value any) int {
	m, ok := value.(map[string]any)
	if !ok {
		return 0
	}
	return listLen(m["findings"])
}

func nestedString(value any, key string) string {
	m, ok := value.(map[string]any)
	if !ok {
		return ""
	}
	return eventString(m[key])
}
