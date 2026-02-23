package history

import "time"

type ToolUse struct {
	Tool      string         `json:"tool,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	Timestamp *time.Time     `json:"timestamp,omitempty"`
	CWD       string         `json:"cwd,omitempty"`
	SessionID string         `json:"session_id,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Source    string         `json:"source,omitempty"` // "claude" or "codex"
}

type Filter struct {
	Tool   string
	Source string // "claude", "codex", or "" for all
	Since  *time.Time
	Before *time.Time
	Limit  int
}

type SessionEntry struct {
	ToolUse   ToolUse `json:"tool_use,omitempty"`
	Message   Message `json:"message,omitempty"`
	Timestamp string  `json:"timestamp,omitempty"`
	CWD       string  `json:"cwd,omitempty"`
	SessionID string  `json:"sessionId,omitempty"`
}

type Message struct {
	Content []Content `json:"content,omitempty"`
}

type Content struct {
	Type  string         `json:"type,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
	ID    string         `json:"id,omitempty"`
}
