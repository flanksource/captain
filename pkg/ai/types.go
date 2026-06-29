package ai

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// Request is one model/agent call. It is a type alias for the serializable
// api.Spec (model, prompt, budget, memory, permissions, context, session,
// turns) — ai.Request IS the spec, so providers read the nested fields directly:
// req.Temperature/req.Effort (Model is inlined), req.Prompt.User,
// req.Permissions.Mode, req.Context.Dir, req.Memory.Skills. The structured-output
// Go type rides on Prompt.Schema; the
// runtime-only tool-permission broker callback lives on Config.CanUseTool.
type Request = api.Spec

// PermissionFunc decides whether an agent may run a tool. It is invoked by a
// streaming provider on a can_use_tool control request; returning an error denies
// the tool with the error text fed back to the agent as the reason.
type PermissionFunc func(ctx context.Context, req PermissionRequest) (PermissionDecision, error)

// PermissionRequest describes the tool an agent wants to run. SessionID is filled
// in by the provider from the live session so a caller can key approvals by it.
type PermissionRequest struct {
	Tool      string
	Input     map[string]any
	ToolUseID string
	SessionID string
}

// PermissionDecision is the answer to a PermissionRequest. On Allow the tool runs
// (with UpdatedInput substituted when non-nil); otherwise it is denied and Message
// is fed back to the agent as the reason.
type PermissionDecision struct {
	Allow        bool
	Message      string
	UpdatedInput map[string]any
}

type Response struct {
	Text           string
	StructuredData any
	Model          string
	Backend        Backend
	Usage          Usage
	Duration       time.Duration
	CacheHit       bool
	Raw            any
}

// Usage is an alias for the canonical api.Usage (per-call token breakdown).
type Usage = api.Usage

type EventKind string

const (
	EventText       EventKind = "text"
	EventThinking   EventKind = "thinking"
	EventToolUse    EventKind = "tool_use"
	EventToolResult EventKind = "tool_result"
	EventResult     EventKind = "result"
	EventError      EventKind = "error"
	EventSystem     EventKind = "system"
	// EventPermission surfaces a tool-permission request brokered via CanUseTool
	// so callers can observe what is awaiting approval. Tool/Input/ToolCallID carry
	// the requested tool; the decision itself flows back through the CanUseTool
	// callback, not through the event stream.
	EventPermission EventKind = "permission"
)

type Event struct {
	Kind EventKind
	Text string // text content; tool output when Kind == EventToolResult

	Tool  string         // when Kind == EventToolUse
	Input map[string]any // when Kind == EventToolUse

	// ToolCallID correlates a tool call with its result. Set on EventToolUse
	// (the call) and EventToolResult (its complete output). Backends that stream
	// output incrementally accumulate it and emit a single EventToolResult.
	ToolCallID string

	Usage     *Usage  // when Kind == EventResult
	CostUSD   float64 // when Kind == EventResult
	Success   bool    // when Kind == EventResult; for EventToolResult, false = the tool errored
	SessionID string  // when Kind == EventSystem
	Model     string
	Error     string // when Kind == EventError

	// Raw carries the backend-native event (e.g. claude.HistoryEntry for the
	// claude_cli stream) so renderers can use the rich pretty-printers in
	// pkg/claude/tools instead of reformatting from Tool/Input.
	Raw any
}

// Cost and Costs are aliases for the canonical api types (token + money
// accounting). The methods (Total/Add/Sum/ByModel) live on the api types.
type Cost = api.Cost
type Costs = api.Costs

// Config is the provider construction/runtime config. Model (name/backend/temp/
// effort) and Budget (cost ceiling, max tokens) come from api; the rest are
// transport/runtime concerns that never belong in the serializable Spec.
type Config struct {
	Model         api.Model  // Name, ID, Backend (empty = infer), Temperature, Effort
	Budget        api.Budget // Cost (USD ceiling, 0 = unlimited), MaxTokens
	APIKey        string     // empty = env lookup
	APIURL        string
	CacheDBPath   string
	CacheTTL      time.Duration
	NoCache       bool
	MaxConcurrent int
	SessionID     string
	ProjectName   string

	// CanUseTool, when set, brokers tool permissions over the stream-json control
	// protocol: the streaming provider asks this callback before a tool that needs
	// approval runs, and forwards the decision to the agent. Only providers that
	// support a server→client permission round-trip honour it (claude-agent);
	// others ignore it. A nil callback keeps the auto-approve (bypass) behaviour.
	// It is never serialized (the agent process never sees the Go closure).
	CanUseTool PermissionFunc `json:"-"`
}
