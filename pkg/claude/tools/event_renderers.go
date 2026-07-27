package tools

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
)

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
		text = text.Append(" "+eventPreview(cmd, 120), "text-muted")
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

type UserShellCommandTool struct{ BaseTool }

func (t *UserShellCommandTool) Name() string         { return "UserShellCommand" }
func (t *UserShellCommandTool) Category() string     { return "chat" }
func (t *UserShellCommandTool) FilePath() string     { return "" }
func (t *UserShellCommandTool) ExtractPath() string  { return "" }
func (t *UserShellCommandTool) Detail() api.Textable { return commandOutputDetail(t.BaseTool) }
func (t *UserShellCommandTool) Pretty() api.Text {
	text := eventText(icons.Icon{Unicode: "💻", Iconify: "codicon:terminal", Style: "muted"}, "local command", "text-slate-500 font-medium")
	if cmd := t.Str("command"); cmd != "" {
		text = text.Append(" "+eventPreview(cmd, 120), "text-muted")
	}
	if duration := eventFloat(t.Input["duration_ms"]); duration > 0 {
		text = text.Append(" "+formatDurationMS(duration), "text-gray-500")
	}
	if code := eventInt(t.Input["exit_code"]); code != 0 {
		text = text.Append(fmt.Sprintf(" exit=%d", code), "text-red-500")
	}
	return text
}

type CodexPatchApplyTool struct{ BaseTool }

func (t *CodexPatchApplyTool) Name() string { return "CodexPatchApply" }

// Category is edit, not chat: this row is Codex applying a patch, i.e. a file
// mutation. The other event renderers here are genuinely conversational, but
// grouping a write with them hides Codex file changes from `--category edit`
// and from anything sourcing file modifications off the category.
func (t *CodexPatchApplyTool) Category() string     { return string(bash.CategoryEdit) }
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
		text = text.Append(" "+strings.Trim(server+"."+tool, "."), "text-muted")
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
		text = text.Append(" "+eventPreview(query, 100), "text-muted")
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
		text = text.Append(" "+ShortenPath(path), "text-muted")
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
		text = text.Append(" "+eventPreview(action, 100), "text-muted")
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
		text = text.Append(" "+nick, "text-muted")
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
	if n := listLen(t.Input["names"]); n > 0 {
		return text.Append(fmt.Sprintf(" count=%d", n), "text-gray-500")
	}
	if count := eventInt(t.Input["skillCount"]); count > 0 {
		return text.Append(fmt.Sprintf(" count=%d", count), "text-gray-500")
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
		text = text.Append(fmt.Sprintf(" used=$%.2f", used), "text-muted")
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
		text = text.Append(fmt.Sprintf(" #%d", n), "text-muted")
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
		text = text.Append(" "+name, "text-muted")
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
		text = text.Append(" "+ShortenPath(cwd), "text-muted")
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

// ClaudeCommandTool renders a Claude slash-command invocation (claude_command)
// or its captured output (claude_command_output) as a concise, non-operational
// event row. The wrapper tags are stripped by the reader; this only formats the
// preserved fields.
type ClaudeCommandTool struct{ BaseTool }

func (t *ClaudeCommandTool) Name() string        { return "ClaudeCommand" }
func (t *ClaudeCommandTool) Category() string    { return "chat" }
func (t *ClaudeCommandTool) FilePath() string    { return "" }
func (t *ClaudeCommandTool) ExtractPath() string { return "" }

func (t *ClaudeCommandTool) isOutput() bool {
	return t.Str("event") == "claude_command_output" || t.Str("stream") != ""
}

func (t *ClaudeCommandTool) Pretty() api.Text {
	if t.isOutput() {
		stream := firstNonEmptyEvent(t.Str("stream"), "stdout")
		color := "text-slate-500 font-medium"
		if stream == "stderr" {
			color = "text-red-500 font-medium"
		}
		text := eventText(icons.Terminal, stream, color)
		if content := t.Str("content"); content != "" {
			text = text.Append(" "+eventPreview(content, 100), "text-muted")
		}
		return text
	}
	name := firstNonEmptyEvent(t.Str("command_name"), "command")
	text := eventText(icons.Terminal, name, "text-indigo-500 font-medium")
	if args := t.Str("command_args"); args != "" {
		text = text.Append(" "+eventPreview(args, 100), "text-muted")
	}
	return text
}

func (t *ClaudeCommandTool) Detail() api.Textable {
	if d := t.BaseTool.Detail(); d != nil {
		return d
	}
	body := t.Str("command_args")
	if t.isOutput() {
		body = t.Str("content")
	}
	if strings.TrimSpace(body) == "" {
		return nil
	}
	text := clicky.Text("").Append(body, "")
	return &text
}

// GoalStatusTool renders a session-scoped Claude goal directive as a concise,
// non-operational event row.
type GoalStatusTool struct{ BaseTool }

func (t *GoalStatusTool) Name() string        { return "GoalStatus" }
func (t *GoalStatusTool) Category() string    { return "chat" }
func (t *GoalStatusTool) FilePath() string    { return "" }
func (t *GoalStatusTool) ExtractPath() string { return "" }

func (t *GoalStatusTool) Pretty() api.Text {
	label, color := "goal active", "text-amber-600 font-medium"
	if met, _ := t.Input["met"].(bool); met {
		label, color = "goal met", "text-green-600 font-medium"
	}
	text := eventText(icons.Target, label, color)
	if condition := t.Str("condition"); condition != "" {
		text = text.Append(" "+eventPreview(condition, 100), "text-muted")
	}
	return text
}

func (t *GoalStatusTool) Detail() api.Textable {
	if d := t.BaseTool.Detail(); d != nil {
		return d
	}
	condition := strings.TrimSpace(t.Str("condition"))
	reason := strings.TrimSpace(t.Str("reason"))
	if condition == "" && reason == "" {
		return nil
	}
	text := clicky.Text("")
	if condition != "" {
		text = text.Append("condition: ", "font-bold text-muted").Append(condition, "")
	}
	if reason != "" {
		if condition != "" {
			text = text.NewLine()
		}
		text = text.Append("reason: ", "font-bold text-muted").Append(reason, "")
	}
	return &text
}
