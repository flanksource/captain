package history

import "time"

type CodexEvent struct {
	Timestamp string       `json:"timestamp"`
	Type      string       `json:"type"`
	Payload   CodexPayload `json:"payload"`
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
	ID            string `json:"id,omitempty"`
	CWD           string `json:"cwd,omitempty"`
	CLIVersion    string `json:"cli_version,omitempty"`
	Source        string `json:"source,omitempty"`
	ModelProvider string `json:"model_provider,omitempty"`

	// response_item: function_call
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	CallID    string `json:"call_id,omitempty"`

	// response_item: function_call_output
	Output string `json:"output,omitempty"`

	// response_item: reasoning
	Summary []CodexReasoningSummary `json:"summary,omitempty"`

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
