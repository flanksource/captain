package tools

import (
	"fmt"
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
	case "claude_command", "claude_command_output":
		return "ClaudeCommand"
	case "goal_status":
		return "GoalStatus"
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
		"Informational", "WorktreeState", "Relocated", "Started", "UserShellCommand",
		"ClaudeCommand", "GoalStatus":
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
		text = text.Append(" "+FormatCompactTokens(int(total)), "text-muted")
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
