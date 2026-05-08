package history

import (
	"encoding/json"
	"time"
)

type CodexEvent struct {
	Timestamp string       `json:"timestamp"`
	Type      string       `json:"type"`
	Payload   CodexPayload `json:"payload"`

	// Fields used by the newer dotted-name `codex exec --json` schema.
	// Older rollout-jsonl events nest everything under Payload; newer live
	// events spread fields at the top level. Unmarshal tolerates both.
	ThreadID string           `json:"thread_id,omitempty"`
	Item     *CodexItem       `json:"item,omitempty"`
	Error    *CodexErrorBlock `json:"error,omitempty"`
	Message  string           `json:"message,omitempty"`
	Usage    *CodexLiveUsage  `json:"usage,omitempty"`
}

type CodexItem struct {
	Type    string         `json:"type,omitempty"`
	Text    string         `json:"text,omitempty"`
	Content []CodexContent `json:"content,omitempty"`
	Name    string         `json:"name,omitempty"`
	Role    string         `json:"role,omitempty"`
}

type CodexErrorBlock struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
	Status  int    `json:"status,omitempty"`
}

type CodexLiveUsage struct {
	InputTokens  int `json:"input_tokens,omitempty"`
	OutputTokens int `json:"output_tokens,omitempty"`
}

func (e CodexEvent) Time() *time.Time {
	if e.Timestamp == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, e.Timestamp)
	if err != nil {
		return nil
	}
	return &t
}

type CodexPayload struct {
	Type string `json:"type"`

	// session_meta
	ID            string         `json:"id,omitempty"`
	CWD           string         `json:"cwd,omitempty"`
	CLIVersion    string         `json:"cli_version,omitempty"`
	Source        string         `json:"source,omitempty"`
	ModelProvider string         `json:"model_provider,omitempty"`
	Originator    string         `json:"originator,omitempty"`
	Git           *CodexGitMeta  `json:"git,omitempty"`

	// response_item: function_call
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	CallID    string `json:"call_id,omitempty"`

	// response_item: function_call_output
	Output string `json:"output,omitempty"`

	// response_item: reasoning carries an array of CodexReasoningSummary;
	// turn_context carries a plain string ("none", "auto", etc.). Use a
	// RawMessage so neither shape causes the rest of the payload to fail.
	Summary json.RawMessage `json:"summary,omitempty"`

	// response_item: message
	Role    string         `json:"role,omitempty"`
	Content []CodexContent `json:"content,omitempty"`

	// event_msg: agent_reasoning / agent_message / user_message
	Text    string `json:"text,omitempty"`
	Message string `json:"message,omitempty"`

	// event_msg: token_count
	Info *CodexTokenInfo `json:"info,omitempty"`

	// event_msg: task_started / task_complete
	TurnID string `json:"turn_id,omitempty"`

	// turn_context: model + reasoning configuration set per turn.
	// `effort` is the top-level reasoning effort ("low"/"medium"/"high");
	// older payloads also expose `reasoning_effort` inside collaboration_mode.
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

type CodexGitMeta struct {
	CommitHash    string `json:"commit_hash,omitempty"`
	Branch        string `json:"branch,omitempty"`
	RepositoryURL string `json:"repository_url,omitempty"`
}

type CodexReasoningSummary struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type CodexContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type CodexTokenInfo struct {
	TotalTokenUsage CodexTokenUsage `json:"total_token_usage"`
	LastTokenUsage  CodexTokenUsage `json:"last_token_usage"`
}

type CodexTokenUsage struct {
	InputTokens       int `json:"input_tokens"`
	CachedInputTokens int `json:"cached_input_tokens"`
	OutputTokens      int `json:"output_tokens"`
	TotalTokens       int `json:"total_tokens"`
}
