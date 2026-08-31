package api

import (
	"encoding/json"
	"time"
)

// Response is the result of a buffered (non-streaming) provider execution.
type Response struct {
	Text            string
	StructuredData  any
	TerminalOutcome *TerminalOutcome
	ToolApproval    *ToolApprovalState
	Model           string
	Backend         Backend
	Usage           Usage
	// CostUSD is the response's reported cost: the provider's authoritative value
	// when it supplies one (claude-cli total_cost_usd, claude-agent cost_usd),
	// otherwise the provider's list-price estimate. 0 means no cost was reported
	// (the buffered path used to drop it — see finding D4). Consumers should
	// prefer this over recomputing from tokens.
	CostUSD  float64
	Duration time.Duration
	CacheHit bool
	Raw      any

	// Workspace is the run's working-dir runtime state (cwd, git details, changed
	// files, commits, plan). Populated by the agent runner + worktree hook.
	Workspace *Workspace
}

// EventKind classifies a streaming provider Event.
type EventKind string

const (
	EventText        EventKind = "text"
	EventThinking    EventKind = "thinking"
	EventToolUse     EventKind = "tool_use"
	EventToolResult  EventKind = "tool_result"
	EventResult      EventKind = "result"
	EventError       EventKind = "error"
	EventInterrupted EventKind = "interrupted"
	EventSystem      EventKind = "system"
	// EventPermission surfaces a tool-permission request brokered via CanUseTool
	// so callers can observe what is awaiting approval. Tool/Input/ToolCallID carry
	// the requested tool; the decision itself flows back through the CanUseTool
	// callback, not through the event stream.
	EventPermission EventKind = "permission"
)

// Event is one item in a streaming provider's output channel.
type Event struct {
	Kind EventKind
	Text string // text content; tool output when Kind == EventToolResult

	Tool  string         // when Kind == EventToolUse
	Input map[string]any // when Kind == EventToolUse

	// ToolCallID correlates a tool call with its result. Set on EventToolUse
	// (the call) and EventToolResult (its complete output). Backends that stream
	// output incrementally accumulate it and emit a single EventToolResult.
	ToolCallID string
	// Delegated marks a lifecycle event reconstructed by the supervisor from an
	// authenticated remote MCP call rather than emitted by its local provider.
	Delegated bool `json:"-"`
	// ApprovalID is the durable captain_turn_requests UUID associated with an
	// EventPermission. It is distinct from the provider's tool-call ID.
	ApprovalID string

	Usage     *Usage  // when Kind == EventResult
	CostUSD   float64 // when Kind == EventResult
	Success   bool    // when Kind == EventResult; for EventToolResult, false = the tool errored
	SessionID string  // when Kind == EventSystem
	Model     string
	Error     string // when Kind == EventError
	Reason    string // when Kind == EventInterrupted

	// StructuredData is the validated structured output (raw JSON) carried on an
	// EventResult when the request supplied a schema; nil for text-mode runs. It
	// is raw JSON because the streaming contract does not know the caller's Go
	// type — the buffered Execute path unmarshals it into Request.Prompt.Schema.
	StructuredData json.RawMessage
	ToolApproval   *ToolApprovalState

	// Raw carries the backend-native event (e.g. claude.HistoryEntry for the
	// claude_cli stream) so renderers can use the rich pretty-printers in
	// pkg/claude/tools instead of reformatting from Tool/Input.
	Raw any
}
