package history

import (
	"time"

	"github.com/flanksource/captain/pkg/api"
)

type ToolUse struct {
	Tool            string         `json:"tool,omitempty"`
	Input           map[string]any `json:"input,omitempty"`
	Timestamp       *time.Time     `json:"timestamp,omitempty"`
	CWD             string         `json:"cwd,omitempty"`
	SessionID       string         `json:"session_id,omitempty"`
	TurnID          string         `json:"turn_id,omitempty"`
	ToolUseID       string         `json:"tool_use_id,omitempty"`
	Source          string         `json:"source,omitempty"` // "claude" or "codex"
	Model           string         `json:"model,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	Namespace       string         `json:"namespace,omitempty"`
	InputTokens     int            `json:"input_tokens,omitempty"`
	OutputTokens    int            `json:"output_tokens,omitempty"`
	// ReasoningTokens is disjoint from OutputTokens, per the api.Usage contract:
	// OpenAI reports reasoning as a subset of output, so it is netted out at this
	// parse boundary the way the live providers already net it.
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	TotalTokens     int `json:"total_tokens,omitempty"`
	ContextWindow   int `json:"context_window,omitempty"`
	// CumulativeUsage is the provider's own running total for the session as of
	// this record, rather than this record's delta. It is the result figure:
	// reading the last one is exact, where summing per-record deltas drifts
	// (a real 238-event codex session sums to 29.47M against a reported 29.24M).
	// Netted to the disjoint api.Usage contract like the per-record fields.
	CumulativeUsage *api.Usage `json:"cumulative_usage,omitempty"`
	AgentID         string     `json:"agent_id,omitempty"`
	AgentType       string     `json:"agent_type,omitempty"`
	AgentDesc       string     `json:"agent_desc,omitempty"`
	Response        string     `json:"response,omitempty"`
	RecordType      string     `json:"-"`
	// SourceLine is the 1-based JSONL line the use was extracted from — the
	// line of the FIRST record when several collapse into one row. It is the
	// stable identity of the row across re-parses of a growing transcript;
	// anything derived from position in the output slice is not.
	SourceLine int64 `json:"-"`
	// Provisional marks a row a later pass can still complete: a tool call whose
	// output has not been written yet, or a reasoning span still open at EOF.
	// Ingest must not treat such a row as final, or the correction is dropped.
	Provisional bool `json:"-"`
}

type Filter struct {
	Tool   string
	Source string // "claude", "codex", or "" for all
	Since  *time.Time
	Before *time.Time
	Limit  int
}

type SessionEntry struct {
	Type      string  `json:"type,omitempty"` // top-level entry kind: assistant, user, system, ...
	ToolUse   ToolUse `json:"tool_use,omitempty"`
	Message   Message `json:"message,omitempty"`
	Timestamp string  `json:"timestamp,omitempty"`
	CWD       string  `json:"cwd,omitempty"`
	SessionID string  `json:"sessionId,omitempty"`
	UUID      string  `json:"uuid,omitempty"`
	// IsAPIErrorMessage marks a synthetic assistant entry Claude Code writes when
	// an API request fails (rate limit, auth, server, or network error) after its
	// retries are exhausted. Such an entry ends the turn but is a failure, not a
	// normal completion — its stop_reason ("stop_sequence") must not be read as
	// success. APIErrorStatus is the HTTP status (0 for a connection/network error
	// that never reached the server) and ErrorType is Claude Code's classification
	// (e.g. "rate_limit", "server_error", "authentication_failed").
	IsAPIErrorMessage bool   `json:"isApiErrorMessage,omitempty"`
	APIErrorStatus    int    `json:"apiErrorStatus,omitempty"`
	ErrorType         string `json:"error,omitempty"`
}

type Message struct {
	Role       string    `json:"role,omitempty"`
	StopReason string    `json:"stop_reason,omitempty"`
	Content    []Content `json:"content,omitempty"`
}

type Content struct {
	Type     string         `json:"type,omitempty"`
	Text     string         `json:"text,omitempty"`
	Thinking string         `json:"thinking,omitempty"`
	Name     string         `json:"name,omitempty"`
	Input    map[string]any `json:"input,omitempty"`
	ID       string         `json:"id,omitempty"`
}
