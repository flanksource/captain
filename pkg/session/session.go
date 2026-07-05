package session

import (
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// Session is the unified session aggregate. It is the single source of truth the
// history/sessions commands render, the viewer consumes, and the chat/live
// surfaces project from.
type Session struct {
	ID       string `json:"id"`
	Source   string `json:"source,omitempty"` // "claude" | "codex"
	Project  string `json:"project,omitempty"`
	CWD      string `json:"cwd,omitempty"`
	Slug     string `json:"slug,omitempty"`
	Version  string `json:"version,omitempty"`
	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"` // primary model

	Git       GitState   `json:"git,omitempty"`
	StartedAt *time.Time `json:"startedAt,omitempty"`
	EndedAt   *time.Time `json:"endedAt,omitempty"`

	Usage     api.Usage `json:"usage,omitempty"`
	Cost      api.Cost  `json:"cost,omitempty"`
	ToolCosts api.Costs `json:"toolCosts,omitempty"` // per-model breakdown

	Root   *Agent   `json:"root,omitempty"`   // agent hierarchy tree
	Agents []*Agent `json:"agents,omitempty"` // flat index (root first)

	Messages  []Message     `json:"messages,omitempty"`
	Files     ChangedFiles  `json:"files,omitempty"`
	Plan      *Plan         `json:"plan,omitempty"`
	Approvals ApprovalStats `json:"approvals,omitempty"`
	Health    []Health      `json:"health,omitempty"`
	Live      *LiveProcess  `json:"live,omitempty"`
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
	ID       string    `json:"id,omitempty"`
	ParentID string    `json:"parentId,omitempty"`
	Type     string    `json:"type,omitempty"` // agentType (from meta.json)
	Desc     string    `json:"desc,omitempty"` // task description
	IsRoot   bool      `json:"isRoot,omitempty"`
	Children []*Agent  `json:"children,omitempty"`
	Usage    api.Usage `json:"usage,omitempty"`
	Cost     api.Cost  `json:"cost,omitempty"`
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
