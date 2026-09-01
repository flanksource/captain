package history

import (
	"github.com/segmentio/encoding/json"
	"time"
)

type CodexEvent struct {
	Timestamp string       `json:"timestamp"`
	Type      string       `json:"type"`
	Payload   CodexPayload `json:"payload"`
	// Ordinal is the record's position in the thread's history. A forked or
	// subagent rollout replays its parent's records first; session_meta's
	// subagent_history_start_ordinal marks where the thread's own history begins.
	Ordinal *int `json:"ordinal,omitempty"`

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
	Type string         `json:"type"`
	Raw  map[string]any `json:"-"`

	// session_meta. ID is this thread; SessionID is the root thread of the
	// fork tree (equal to ID for a top-level thread), so a subagent's rollout
	// must never be filed under SessionID.
	ID             string `json:"id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	ForkedFromID   string `json:"forked_from_id,omitempty"`
	ParentThreadID string `json:"parent_thread_id,omitempty"`
	ThreadSource   string `json:"thread_source,omitempty"`
	AgentNickname  string `json:"agent_nickname,omitempty"`
	AgentPath      string `json:"agent_path,omitempty"`
	// SubagentHistoryStartOrdinal is the first ordinal that belongs to this
	// thread; earlier records are the parent's history replayed for context.
	SubagentHistoryStartOrdinal *int   `json:"subagent_history_start_ordinal,omitempty"`
	CWD                         string `json:"cwd,omitempty"`
	CLIVersion                  string `json:"cli_version,omitempty"`
	// Source has appeared as both a scalar (for root sessions) and an object
	// (for example {"subagent":{"other":"guardian"}}). Keep the provider
	// shape opaque so a new source variant cannot invalidate the whole
	// session_meta record and discard its ID.
	Source        json.RawMessage `json:"source,omitempty"`
	ModelProvider string          `json:"model_provider,omitempty"`
	Originator    string          `json:"originator,omitempty"`
	Git           *CodexGitMeta   `json:"git,omitempty"`

	// response_item: function_call
	Name      string          `json:"name,omitempty"`
	Namespace string          `json:"namespace,omitempty"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	CallID    string          `json:"call_id,omitempty"`
	Input     string          `json:"input,omitempty"`
	Metadata  *CodexMetadata  `json:"internal_chat_message_metadata_passthrough,omitempty"`

	// response_item: function_call_output. Codex emits either a JSON string or
	// an ordered array of text content blocks depending on the tool transport.
	Output json.RawMessage            `json:"output,omitempty"`
	Tools  []CodexToolSearchNamespace `json:"tools,omitempty"`

	// response_item: reasoning carries an array of CodexReasoningSummary;
	// turn_context carries a plain string ("none", "auto", etc.). Use a
	// RawMessage so neither shape causes the rest of the payload to fail.
	Summary json.RawMessage `json:"summary,omitempty"`

	// response_item: message
	Role    string         `json:"role,omitempty"`
	Content []CodexContent `json:"content,omitempty"`

	// event_msg: agent_reasoning / agent_message / user_message
	Text                  string `json:"text,omitempty"`
	Message               string `json:"message,omitempty"`
	Phase                 string `json:"phase,omitempty"`
	StartedAt             any    `json:"started_at,omitempty"`
	CompletedAt           int64  `json:"completed_at,omitempty"`
	DurationMS            int64  `json:"duration_ms,omitempty"`
	TimeToFirstTokenMS    int64  `json:"time_to_first_token_ms,omitempty"`
	LastAgentMessage      string `json:"last_agent_message,omitempty"`
	ModelContextWindow    int    `json:"model_context_window,omitempty"`
	CollaborationModeKind string `json:"collaboration_mode_kind,omitempty"`

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

func (p *CodexPayload) UnmarshalJSON(data []byte) error {
	type alias CodexPayload
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*p = CodexPayload(a)
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err == nil {
		p.Raw = raw
	}
	return nil
}

type CodexGitMeta struct {
	CommitHash    string `json:"commit_hash,omitempty"`
	Branch        string `json:"branch,omitempty"`
	RepositoryURL string `json:"repository_url,omitempty"`
}

type CodexMetadata struct {
	TurnID string `json:"turn_id,omitempty"`
}

type CodexToolSearchNamespace struct {
	Type        string                 `json:"type,omitempty"`
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Tools       []CodexToolSearchEntry `json:"tools,omitempty"`
}

type CodexToolSearchEntry struct {
	Type        string         `json:"type,omitempty"`
	Name        string         `json:"name,omitempty"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
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
	TotalTokenUsage    CodexTokenUsage `json:"total_token_usage"`
	LastTokenUsage     CodexTokenUsage `json:"last_token_usage"`
	ModelContextWindow int             `json:"model_context_window,omitempty"`
}

type CodexTokenUsage struct {
	InputTokens           int `json:"input_tokens"`
	CachedInputTokens     int `json:"cached_input_tokens"`
	OutputTokens          int `json:"output_tokens"`
	ReasoningOutputTokens int `json:"reasoning_output_tokens"`
	TotalTokens           int `json:"total_tokens"`
}
