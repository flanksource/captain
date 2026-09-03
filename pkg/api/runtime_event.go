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
	Runtime         Runtime
	Usage           Usage
	// CostUSD is the response's reported cost: the provider's authoritative value
	// when it supplies one (the claude CLI's total_cost_usd, the agent's cost_usd),
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

	// EventVerified and EventVerifyFailed carry one verify hook's verdict on the
	// turn that just finished — the loop's definition of done, reported as it is
	// reached. Tool names the check, Text is the report, Duration is its wall
	// clock, and on a failure Text carries the output the next turn will be given.
	//
	// They are distinct kinds rather than an EventSystem line because the verdict
	// is the run's outcome, not a narration of it: a renderer has to colour a pass
	// and a failure differently, a dashboard filters on them, and a transcript
	// reader must be able to find them without matching on prose.
	EventVerified     EventKind = "verified"
	EventVerifyFailed EventKind = "verify_failed"

	// EventVerifyProgress is one in-flight snapshot of a check that has not
	// reached a verdict yet: a fixture run's tests as they land, rate-limited so
	// a chatty runner does not flood the stream. Tool names the check and Raw
	// carries the same *VerifyReport the verdict will, so a renderer redraws the
	// tree from one shape whether the check is running or done.
	EventVerifyProgress EventKind = "verify_progress"
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
	// ApprovalID is the durable captain_turn_requests UUID associated with an
	// EventPermission. It is distinct from the provider's tool-call ID.
	ApprovalID string

	Usage     *Usage  // when Kind == EventResult
	CostUSD   float64 // when Kind == EventResult
	Success   bool    // when Kind == EventResult; for EventToolResult, false = the tool errored
	SessionID string  // when Kind == EventSystem
	Model     string
	Error     string // when Kind == EventError
	Reason    string // when Kind == EventInterrupted or EventVerifyFailed

	// Duration is how long the reported work took, when the event reports a
	// completed unit of work rather than a fragment of one (EventVerified /
	// EventVerifyFailed). Zero elsewhere.
	Duration time.Duration

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
