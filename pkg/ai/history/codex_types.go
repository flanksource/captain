package history

import (
	"github.com/segmentio/encoding/json"
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

	// raw is the payload object verbatim, decoded into a map only by the two
	// record shapes that pass unknown keys through (event_msg and world_state).
	// Eagerly decoding every payload into map[string]any cost half of all
	// allocations in a transcript parse -- 51% of 114 MB for a 10k-line
	// rollout -- to serve a minority of records.
	raw json.RawMessage `json:"-"`

	// session_meta
	ID         string `json:"id,omitempty"`
	CWD        string `json:"cwd,omitempty"`
	CLIVersion string `json:"cli_version,omitempty"`
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
	// Decode through the alias straight into p. Decoding into a temporary and
	// copying it back cost a second 544-byte struct per record -- 7% of a
	// transcript parse's allocations -- for a value that was thrown away.
	// The explicit zeroing is what the copy used to provide: a payload the
	// decoder is handed twice must not keep the first record's fields.
	type alias CodexPayload
	*p = CodexPayload{}
	if err := json.Unmarshal(data, (*alias)(p)); err != nil {
		return err
	}
	// Copy rather than alias: the decoder owns data and may reuse the buffer.
	p.raw = append(json.RawMessage(nil), data...)
	return nil
}

// RawMap decodes the payload's unknown keys on demand. It returns nil for a
// payload that is not a JSON object, matching the eager decode it replaced:
// callers range over the result and a nil map ranges zero times.
func (p CodexPayload) RawMap() map[string]any {
	if len(p.raw) == 0 {
		return nil
	}
	var raw map[string]any
	if err := json.Unmarshal(p.raw, &raw); err != nil {
		return nil
	}
	return raw
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
