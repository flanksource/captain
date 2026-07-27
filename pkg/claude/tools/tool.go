package tools

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/api/icons"
	"github.com/segmentio/encoding/json"
)

// Tool is the interface all tool types implement.
type Tool interface {
	Name() string
	Category() string
	Pretty() api.Text
	Detail() api.Textable
	FilePath() string
	ExtractPath() string
	Base() *BaseTool
}

type ModelUsage struct {
	Model                    string  `json:"model,omitempty"`
	InputTokens              int     `json:"inputTokens,omitempty"`
	OutputTokens             int     `json:"outputTokens,omitempty"`
	CacheCreationInputTokens int     `json:"cacheCreationInputTokens,omitempty"`
	CacheReadInputTokens     int     `json:"cacheReadInputTokens,omitempty"`
	ServiceTier              string  `json:"serviceTier,omitempty"`
	Cost                     float64 `json:"cost,omitempty"`
}

func (m ModelUsage) TotalTokens() int {
	return m.InputTokens + m.OutputTokens
}

func (m ModelUsage) Pretty() api.Text {
	parts := []string{}
	if m.InputTokens > 0 {
		parts = append(parts, "In:"+FormatCompactTokens(m.InputTokens))
	}
	if m.OutputTokens > 0 {
		parts = append(parts, "Out:"+FormatCompactTokens(m.OutputTokens))
	}
	if m.CacheReadInputTokens > 0 {
		parts = append(parts, "Cache:"+FormatCompactTokens(m.CacheReadInputTokens))
	}
	if m.Cost > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", m.Cost))
	}
	if len(parts) == 0 {
		return clicky.Text("")
	}
	text := "(" + strings.Join(parts, " ") + ")"
	return clicky.Text("").Append(text, "text-gray-500")
}

type Models []ModelUsage

func (m Models) TotalCost() float64 {
	var total float64
	for _, mu := range m {
		total += mu.Cost
	}
	return total
}

func (m Models) TotalTokens() int {
	var total int
	for _, mu := range m {
		total += mu.TotalTokens()
	}
	return total
}

func (m Models) TotalInput() int {
	var total int
	for _, mu := range m {
		total += mu.InputTokens
	}
	return total
}

func (m Models) TotalOutput() int {
	var total int
	for _, mu := range m {
		total += mu.OutputTokens
	}
	return total
}

func (m Models) TotalCacheRead() int {
	var total int
	for _, mu := range m {
		total += mu.CacheReadInputTokens
	}
	return total
}

func (m Models) Pretty() api.Text {
	if len(m) == 0 {
		return clicky.Text("")
	}
	if len(m) == 1 {
		return m[0].Pretty()
	}
	parts := []string{}
	if in := m.TotalInput(); in > 0 {
		parts = append(parts, "In:"+FormatCompactTokens(in))
	}
	if out := m.TotalOutput(); out > 0 {
		parts = append(parts, "Out:"+FormatCompactTokens(out))
	}
	if cost := m.TotalCost(); cost > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", cost))
	}
	if len(parts) == 0 {
		return clicky.Text("")
	}
	return clicky.Text("").Append(fmt.Sprintf("(%d models: %s)", len(m), strings.Join(parts, " ")), "text-gray-500")
}

type BaseTool struct {
	RawTool         string          `json:"tool,omitempty"`
	Input           map[string]any  `json:"input,omitempty"`
	Timestamp       *time.Time      `json:"timestamp,omitempty"`
	CWD             string          `json:"cwd,omitempty"`
	SessionID       string          `json:"sessionId,omitempty"`
	ToolUseID       string          `json:"toolUseId,omitempty"`
	ProjectRoot     string          `json:"projectRoot,omitempty"`
	Denied          bool            `json:"denied,omitempty"`
	DeniedReason    string          `json:"deniedReason,omitempty"`
	IsError         bool            `json:"isError,omitempty"`
	Response        string          `json:"response,omitempty"`
	Models          Models          `json:"models,omitempty"`
	Source          string          `json:"source,omitempty"`
	ReasoningEffort string          `json:"reasoningEffort,omitempty"`
	IsSidechain     bool            `json:"isSidechain,omitempty"`
	AgentID         string          `json:"agentId,omitempty"`
	AgentType       string          `json:"agentType,omitempty"`
	AgentDesc       string          `json:"agentDesc,omitempty"`
	RawEntry        json.RawMessage `json:"-"`
}

func (b *BaseTool) Base() *BaseTool { return b }

func (b *BaseTool) Str(key string) string {
	if v, ok := b.Input[key].(string); ok {
		return v
	}
	return ""
}

func (b *BaseTool) Float(key string) float64 {
	if v, ok := b.Input[key].(float64); ok {
		return v
	}
	return 0
}

func (b *BaseTool) Rel(path string) string {
	if b.ProjectRoot != "" && strings.HasPrefix(path, b.ProjectRoot+"/") {
		return strings.TrimPrefix(path, b.ProjectRoot+"/")
	}
	return ShortenPath(path)
}

func (b *BaseTool) FilePath() string {
	return b.Str("file_path")
}

func (b *BaseTool) ExtractPath() string { return "" }
func (b *BaseTool) Category() string    { return "" }
func (b *BaseTool) Detail() api.Textable {
	if b.Denied && b.DeniedReason != "" {
		text := clicky.Text("").Append("User: ", "font-bold text-red-500").Append(b.DeniedReason, "")
		return &text
	}
	return nil
}

func (b *BaseTool) PrettyTimestamp() string {
	return api.TimeAgo(b.Timestamp).String()
}

func (b *BaseTool) IsDenied() bool { return b.Denied }

func (b *BaseTool) UsagePretty() api.Text {
	return b.Models.Pretty()
}

// header builds "icon label" prefix used by most tools
func (b *BaseTool) header(icon icons.Icon, label, color string) api.Text {
	return clicky.Text("").Add(icon).Append(" "+label, color)
}

// NewTool creates the appropriate Tool implementation from a BaseTool.
func NewTool(base BaseTool) Tool {
	if isPlanFile(base) {
		return &PlanTool{BaseTool: base}
	}
	switch base.RawTool {
	case "Bash":
		return &BashTool{BaseTool: base}
	case "Read":
		return &ReadTool{BaseTool: base}
	case "Write":
		return &WriteTool{BaseTool: base}
	case "Edit":
		return &EditTool{BaseTool: base}
	case "MultiEdit":
		return &MultiEditTool{BaseTool: base}
	case "Grep":
		return &GrepTool{BaseTool: base}
	case "Glob":
		return &GlobTool{BaseTool: base}
	case "Find":
		return &FindTool{BaseTool: base}
	case "Ls":
		return &LsTool{BaseTool: base}
	case "WebFetch":
		return &WebFetchTool{BaseTool: base}
	case "WebSearch":
		return &WebSearchTool{BaseTool: base}
	case "Agent", "Task":
		return &AgentTool{BaseTool: base}
	case "TaskCreate":
		return &TaskCreateTool{BaseTool: base}
	case "TodoWrite":
		return &TodoWriteTool{BaseTool: base}
	case "AskUserQuestion":
		return &AskTool{BaseTool: base}
	case "ExitPlanMode":
		return &ExitPlanTool{BaseTool: base}
	case "Plan":
		return &PlanTool{BaseTool: base}
	case "User":
		return &UserTool{BaseTool: base}
	case "Assistant":
		return &AssistantTool{BaseTool: base}
	case "Reasoning":
		return &ReasoningTool{BaseTool: base}
	case "Event":
		return &EventTool{BaseTool: base}
	case "TokenCount":
		return &TokenCountTool{BaseTool: base}
	case "TaskStarted":
		return &TaskStartedTool{BaseTool: base}
	case "TaskComplete":
		return &TaskCompleteTool{BaseTool: base}
	case "TurnAborted", "ContextCompacted", "ThreadRolledBack", "ItemCompleted":
		return &LifecycleEventTool{BaseTool: base}
	case "MemoryCitation":
		return &EventTool{BaseTool: base}
	case "CodexExecCommand":
		return &CodexExecCommandTool{BaseTool: base}
	case "UserShellCommand":
		return &UserShellCommandTool{BaseTool: base}
	case "CodexPatchApply":
		return &CodexPatchApplyTool{BaseTool: base}
	case "MCPToolCall":
		return &MCPToolCallTool{BaseTool: base}
	case "WebSearchEvent":
		return &WebSearchEventTool{BaseTool: base}
	case "ViewImage":
		return &ViewImageTool{BaseTool: base}
	case "GuardianAssessment":
		return &GuardianAssessmentTool{BaseTool: base}
	case "ReviewMode":
		return &ReviewModeTool{BaseTool: base}
	case "CollabAgentSpawn", "CollabAgentInteraction", "CollabWaiting", "CollabClose":
		return &CollabEventTool{BaseTool: base}
	case "QueueOperation":
		return &QueueOperationTool{BaseTool: base}
	case "DeferredToolsDelta":
		return &DeferredToolsDeltaTool{BaseTool: base}
	case "AgentListingDelta":
		return &AgentListingDeltaTool{BaseTool: base}
	case "SkillListing":
		return &SkillListingTool{BaseTool: base}
	case "Budget":
		return &BudgetTool{BaseTool: base}
	case "PrLink":
		return &PrLinkTool{BaseTool: base}
	case "CompactBoundary", "LocalCommand", "ScheduledTaskFire", "Informational":
		return &ContentEventTool{BaseTool: base}
	case "ClaudeCommand":
		return &ClaudeCommandTool{BaseTool: base}
	case "GoalStatus":
		return &GoalStatusTool{BaseTool: base}
	case "WorktreeState":
		return &WorktreeStateTool{BaseTool: base}
	case "Relocated":
		return &RelocatedTool{BaseTool: base}
	case "Started":
		return &StartedTool{BaseTool: base}
	case "Skill":
		return &SkillTool{BaseTool: base}
	case "SessionInit":
		return &SystemInitTool{BaseTool: base}
	case "HookStart":
		return &HookStartedTool{BaseTool: base}
	case "HookResponse":
		return &HookResponseTool{BaseTool: base}
	case "Result":
		return &ResultSummaryTool{BaseTool: base}
	case "StopHookSummary":
		return &StopHookSummaryTool{BaseTool: base}
	case "TurnDuration":
		return &TurnDurationTool{BaseTool: base}
	case "AwaySummary":
		return &AwaySummaryTool{BaseTool: base}
	case "SessionTitle":
		return &SessionTitleTool{BaseTool: base}
	case "ApiError":
		return &ApiErrorTool{BaseTool: base}
	case "ParseError":
		return &ParseErrorTool{BaseTool: base}
	default:
		return &GenericTool{BaseTool: base}
	}
}

func isPlanFile(base BaseTool) bool {
	if base.RawTool != "Write" && base.RawTool != "Edit" && base.RawTool != "Read" {
		return false
	}
	if fp, ok := base.Input["file_path"].(string); ok {
		return strings.Contains(fp, "/.claude/plans/")
	}
	return false
}

// FormatCompactTokens formats token counts as "1.5M", "2.3k", or raw number.
func FormatCompactTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// ShortenPath converts a path to its shortest representation.
func ShortenPath(path string) string {
	if path == "" {
		return ""
	}
	home, _ := filepath.Abs(filepath.Join("~"))
	if strings.HasPrefix(path, home) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// EstimateTokens estimates token count from text using ~4 characters per token.
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	return (len(text) + 3) / 4
}

// EstimateContentTokens estimates tokens from JSON raw message.
func EstimateContentTokens(content json.RawMessage) int {
	if len(content) == 0 {
		return 0
	}
	return EstimateTokens(string(content))
}
