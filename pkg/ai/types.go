package ai

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

type Request struct {
	SystemPrompt       string
	AppendSystemPrompt string // claude --append-system-prompt
	Prompt             string
	MaxTokens          int
	Temperature        float64
	StructuredOutput   any               // nil = text mode, non-nil = JSON schema target
	Metadata           map[string]string // arbitrary caller metadata

	// Source identifies the prompt's origin (e.g. the .prompt filename), purely
	// for diagnostics — the logging middleware prints it so callers can tell which
	// template produced a request. Empty means unknown.
	Source string

	// Cwd runs the CLI provider's subprocess in this working directory. Empty
	// means the calling process's cwd (the prior behaviour), so existing callers
	// are unaffected. Honoured by the streaming CLI providers (claude_cli, codex_cli).
	Cwd string

	// Per-request CLI knobs honoured by ExecuteStream-capable providers
	// (currently claude_cli). Zero values are equivalent to "let the
	// provider/CLI use its default" so existing buffered Execute callers
	// stay byte-identical.
	SessionID       string // resume an existing session (claude --session-id)
	PermissionMode  string // claude --permission-mode (e.g. "acceptEdits")
	MaxTurns        int    // claude --max-turns (0 = omit, let CLI default)
	ReasoningEffort string // codex -c model_reasoning_effort=... ("low" | "medium" | "high"); other providers ignore

	// Safety / sandbox knobs. Zero values mean "use provider/CLI default".
	// Each provider translates what it understands; unknowns are ignored or
	// surfaced as a config error.
	Edit            bool     // shorthand: acceptEdits + curated Read/Edit/Write/Glob/Grep allowlist
	AllowedTools    []string // claude --allowedTools / codex: not supported
	DisallowedTools []string // claude --disallowedTools / codex: not supported
	NoMCP           bool     // claude --strict-mcp-config + empty inline / codex -c mcp_servers={}
	NoHooks         bool     // claude: requires --bare or --setting-sources / codex --ignore-rules
	NoSkills        bool     // claude --disable-slash-commands / codex --ignore-rules (best effort)
	SkillDirs       []string // claude --plugin-dir (repeatable)
	NoUser          bool     // claude --setting-sources without "user" / codex --ignore-user-config
	NoProject       bool     // claude --setting-sources without "project,local" / codex --ignore-rules
	NoMemory        bool     // claude: requires --bare / codex --ephemeral
	Bare            bool     // claude --bare / codex composite (--ignore-user-config --ignore-rules --ephemeral)

	// CanUseTool, when set, brokers tool permissions over the stream-json control
	// protocol: the streaming provider asks this callback before a tool that needs
	// approval runs, and forwards the decision to the agent. Only providers that
	// support a server→client permission round-trip honour it (claude-agent);
	// others ignore it. A nil callback keeps the auto-approve (bypass) behaviour.
	// It is never serialized (the agent process never sees the Go closure).
	CanUseTool PermissionFunc `json:"-"`
}

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

type Config struct {
	Model         string
	Backend       Backend // empty = infer from model
	APIKey        string  // empty = env lookup
	APIURL        string
	MaxTokens     int
	Temperature   float64
	CacheDBPath   string
	CacheTTL      time.Duration
	NoCache       bool
	MaxConcurrent int
	SessionID     string
	ProjectName   string
	BudgetUSD     float64 // 0 = no budget
}
