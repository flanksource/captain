package history

import "time"

type ToolUse struct {
	Tool            string         `json:"tool,omitempty"`
	Input           map[string]any `json:"input,omitempty"`
	Timestamp       *time.Time     `json:"timestamp,omitempty"`
	CWD             string         `json:"cwd,omitempty"`
	SessionID       string         `json:"session_id,omitempty"`
	ToolUseID       string         `json:"tool_use_id,omitempty"`
	Source          string         `json:"source,omitempty"` // "claude" or "codex"
	Model           string         `json:"model,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	Response        string         `json:"response,omitempty"`
	RecordType      string         `json:"-"`
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
