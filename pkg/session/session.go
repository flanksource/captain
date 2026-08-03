package session

import (
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/segmentio/encoding/json"
)

// Session is the unified session aggregate. It is the single source of truth the
// history/sessions commands render, the viewer consumes, and the chat/live
// surfaces project from.
type Session struct {
	ID                string          `json:"id"`
	ProviderSessionID string          `json:"providerSessionId,omitempty"`
	Revision          int64           `json:"revision"`
	LifecycleStatus   string          `json:"lifecycleStatus,omitempty"`
	ActivityState     string          `json:"activityState,omitempty"`
	HealthState       string          `json:"healthState,omitempty"`
	StateReason       string          `json:"stateReason,omitempty"`
	Source            string          `json:"source,omitempty"` // "claude" | "codex"
	Project           string          `json:"project,omitempty"`
	CWD               string          `json:"cwd,omitempty"`
	Slug              string          `json:"slug,omitempty"`
	Title             string          `json:"title,omitempty"`
	InitialPrompt     string          `json:"initialPrompt,omitempty"`
	Version           string          `json:"version,omitempty"`
	Provider          string          `json:"provider,omitempty"`
	Backend           string          `json:"backend,omitempty"`
	ExecutionMode     api.RuntimeMode `json:"executionMode,omitempty"`
	Model             string          `json:"model,omitempty"` // primary model
	ReasoningEffort   string          `json:"reasoningEffort,omitempty"`
	HistoryFile       string          `json:"historyFile,omitempty"`

	Git       GitState   `json:"git,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`

	Usage     api.Usage `json:"usage,omitempty"`
	Cost      api.Cost  `json:"cost,omitempty"`
	ToolCosts api.Costs `json:"toolCosts,omitempty"` // per-model breakdown
	Context   *Context  `json:"context,omitempty"`
	Budget    *Budget   `json:"budget,omitempty"`

	Capabilities Capabilities `json:"capabilities,omitempty"`
	Events       []Event      `json:"events,omitempty"`
	Turns        []Turn       `json:"turns,omitempty"`

	Root   *Agent   `json:"root,omitempty"`   // agent hierarchy tree
	Agents []*Agent `json:"agents,omitempty"` // flat index (root first)

	Messages  []Message         `json:"messages,omitempty"`
	Requests  []Request         `json:"requests,omitempty"`
	Window    *TranscriptWindow `json:"window,omitempty"`
	Files     ChangedFiles      `json:"files,omitempty"`
	Todos     []tools.TodoItem  `json:"todos,omitempty"`
	Plan      *Plan             `json:"plan,omitempty"`
	Approvals ApprovalStats     `json:"approvals,omitempty"`
	Health    []Health          `json:"health,omitempty"`
	Live      *LiveProcess      `json:"live,omitempty"`

	// Prompt is the realized prompt that launched this session (opaque JSON of
	// the render result), attached from the persistent store for captain-launched
	// sessions; nil for external sessions.
	Prompt json.RawMessage `json:"prompt,omitempty"`

	// StructuredOutput is the decoded object returned by a schema-constrained
	// prompt run. Messages retain the JSON text for transcript compatibility.
	StructuredOutput map[string]any `json:"structuredOutput,omitempty"`
}

// TranscriptWindow records the session's real transcript size when Messages
// and Events hold only a slice of it (see --offset/--limit/--tail). Without it
// a bounded view would report the window's size as the session's own totals.
type TranscriptWindow struct {
	Messages  int `json:"messages"`
	Events    int `json:"events"`
	ToolCalls int `json:"toolCalls"`
}

// GitState is the git/workflow state captured for a session.
type GitState struct {
	Branch   string `json:"branch,omitempty"`
	Commit   string `json:"commit,omitempty"` // codex transcripts
	Worktree string `json:"worktree,omitempty"`
	Diff     string `json:"diff,omitempty"`
}

// Agent is one node in the session's agent hierarchy. The root node represents
// the top-level session; children are sub-agents (Task/Agent spawns).
type Agent struct {
	ID          string    `json:"id,omitempty"`
	ParentID    string    `json:"parentId,omitempty"`
	Type        string    `json:"type,omitempty"` // agentType (from meta.json)
	Desc        string    `json:"desc,omitempty"` // task description
	IsRoot      bool      `json:"isRoot,omitempty"`
	HistoryFile string    `json:"historyFile,omitempty"`
	Children    []*Agent  `json:"children,omitempty"`
	Usage       api.Usage `json:"usage,omitempty"`
	Cost        api.Cost  `json:"cost,omitempty"`
}

// Context is the context-window occupancy for a session or turn.
type Context struct {
	UsedTokens   int `json:"usedTokens,omitempty"`
	WindowTokens int `json:"windowTokens,omitempty"`
	FreePercent  int `json:"freePercent"`
}

// Budget is the latest budget state observed in the transcript.
type Budget struct {
	Used      float64    `json:"used,omitempty"`
	Total     float64    `json:"total,omitempty"`
	Remaining float64    `json:"remaining,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

// Capabilities is the session-level discovery state surfaced by Claude Code.
type Capabilities struct {
	Tools             []string `json:"tools,omitempty"`
	PendingMCPServers []string `json:"pendingMcpServers,omitempty"`
	Agents            []string `json:"agents,omitempty"`
	Skills            []string `json:"skills,omitempty"`
}

// Event is a non-message transcript event captured at session or turn scope.
type Event struct {
	Type      string         `json:"type"`
	Scope     string         `json:"scope,omitempty"`
	TurnID    string         `json:"turnId,omitempty"`
	Timestamp *time.Time     `json:"timestamp,omitempty"`
	UUID      string         `json:"uuid,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
}

// Turn groups user/assistant messages, tool calls, usage, cost, and contextual
// state for one model turn.
type Turn struct {
	ID              string     `json:"id"`
	Status          string     `json:"status,omitempty"`
	AgentID         string     `json:"agentId,omitempty"`
	Index           int        `json:"index"`
	StartedAt       *time.Time `json:"startedAt,omitempty"`
	EndedAt         *time.Time `json:"endedAt,omitempty"`
	StopReason      string     `json:"stopReason,omitempty"`
	Model           string     `json:"model,omitempty"`
	Backend         string     `json:"backend,omitempty"`
	ReasoningEffort string     `json:"reasoningEffort,omitempty"`
	MessageIDs      []string   `json:"messageIds,omitempty"`
	Usage           api.Usage  `json:"usage,omitempty"`
	Cost            api.Cost   `json:"cost,omitempty"`
	Context         *Context   `json:"context,omitempty"`
	Budget          *Budget    `json:"budget,omitempty"`
	Events          []Event    `json:"events,omitempty"`
}

// ChangedFiles is the read/write file set aggregated across a session,
// distinguishing reads from writes (the review's changed-files gap).
type ChangedFiles struct {
	Read    []string `json:"read,omitempty"`
	Written []string `json:"written,omitempty"`
}

// PlanEventKind classifies a plan-lifecycle event.
type PlanEventKind string

const (
	PlanEnter  PlanEventKind = "enter"
	PlanExit   PlanEventKind = "exit"
	PlanWrite  PlanEventKind = "write"
	PlanDenied PlanEventKind = "denied"
)

// PlanEvent is a single plan-lifecycle occurrence in the transcript.
type PlanEvent struct {
	Kind      PlanEventKind `json:"kind"`
	Timestamp *time.Time    `json:"timestamp,omitempty"`
	Reason    string        `json:"reason,omitempty"` // denial reason for PlanDenied
}

// Plan is the exit-plan-mode plan associated with a session plus its lifecycle
// events.
type Plan struct {
	Path     string      `json:"path,omitempty"`
	Slug     string      `json:"slug,omitempty"`
	Content  string      `json:"content,omitempty"`
	Explicit bool        `json:"explicit,omitempty"`
	Events   []PlanEvent `json:"events,omitempty"`
}

// Denial is a single tool denial with its user-supplied reason.
type Denial struct {
	ToolUseID string `json:"toolUseId,omitempty"`
	Tool      string `json:"tool,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

// ApprovalStats aggregates tool approvals/denials for a session.
type ApprovalStats struct {
	Approved int      `json:"approved"`
	Denied   int      `json:"denied"`
	Denials  []Denial `json:"denials,omitempty"`
}

// Health is a derived health signal for a session (low context, cost spike,
// zombie/stopped process, idle).
type Health struct {
	Kind     string `json:"kind"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
}

// LiveProcess describes the OS process backing a live session, when matched.
type LiveProcess struct {
	PID           int        `json:"pid,omitempty"`
	Status        string     `json:"status,omitempty"`
	Active        bool       `json:"active"`
	CPUPercent    float64    `json:"cpuPercent,omitempty"`
	MemoryPercent float64    `json:"memoryPercent,omitempty"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	CWD           string     `json:"cwd,omitempty"`
	Command       string     `json:"command,omitempty"`
}
