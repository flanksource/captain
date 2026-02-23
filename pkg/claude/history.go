package claude

import (
	"encoding/json"
	"time"
)

// HistoryEntry represents a single line in a JSONL transcript
type HistoryEntry struct {
	ParentUUID string  `json:"parentUuid,omitempty"`
	SessionID  string  `json:"sessionId"`
	Version    string  `json:"version,omitempty"`
	Message    Message `json:"message"`
	UUID       string  `json:"uuid"`
	Timestamp  string  `json:"timestamp"`
}

// Message represents a conversation message
type Message struct {
	Model      string         `json:"model,omitempty"`
	Role       MessageRole    `json:"role"`
	Content    []ContentBlock `json:"-"`
	StopReason StopReason     `json:"stop_reason,omitempty"`
	Usage      *Usage         `json:"usage,omitempty"`
}

// UnmarshalJSON handles polymorphic content field (string, array, or null)
func (m *Message) UnmarshalJSON(data []byte) error {
	type messageAlias struct {
		Model      string          `json:"model,omitempty"`
		Role       MessageRole     `json:"role"`
		Content    json.RawMessage `json:"content"`
		StopReason StopReason      `json:"stop_reason,omitempty"`
		Usage      *Usage          `json:"usage,omitempty"`
	}

	var alias messageAlias
	if err := json.Unmarshal(data, &alias); err != nil {
		return err
	}

	m.Model = alias.Model
	m.Role = alias.Role
	m.StopReason = alias.StopReason
	m.Usage = alias.Usage

	if len(alias.Content) == 0 || string(alias.Content) == "null" {
		return nil
	}

	if alias.Content[0] == '"' {
		var text string
		if err := json.Unmarshal(alias.Content, &text); err != nil {
			return err
		}
		m.Content = []ContentBlock{{Type: ContentTypeText, Text: text}}
		return nil
	}

	return json.Unmarshal(alias.Content, &m.Content)
}

// ContentBlock represents a single content item in a message
type ContentBlock struct {
	Type      ContentType     `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// Usage tracks token consumption
type Usage struct {
	InputTokens              int             `json:"input_tokens"`
	OutputTokens             int             `json:"output_tokens"`
	CacheCreationInputTokens int             `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int             `json:"cache_read_input_tokens,omitempty"`
	ServiceTier              string          `json:"service_tier,omitempty"`
	CacheCreation            *CacheCreation  `json:"cache_creation,omitempty"`
	ServerToolUse            *ServerToolUse  `json:"server_tool_use,omitempty"`
}

// CacheCreation contains detailed cache creation breakdown
type CacheCreation struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens,omitempty"`
}

// ServerToolUse tracks server-side tool usage
type ServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests,omitempty"`
}

// IsUserMessage returns true if this is a user message
func (e *HistoryEntry) IsUserMessage() bool {
	return e.Message.Role == MessageRoleUser
}

// IsAssistantMessage returns true if this is an assistant message
func (e *HistoryEntry) IsAssistantMessage() bool {
	return e.Message.Role == MessageRoleAssistant
}

// GetTextContent returns concatenated text from all text blocks
func (m *Message) GetTextContent() string {
	var result string
	for _, block := range m.Content {
		if block.Type == ContentTypeText {
			result += block.Text
		}
	}
	return result
}

// GetToolUses returns all tool_use content blocks
func (m *Message) GetToolUses() []ContentBlock {
	var uses []ContentBlock
	for _, block := range m.Content {
		if block.Type == ContentTypeToolUse {
			uses = append(uses, block)
		}
	}
	return uses
}

// GetToolResults returns all tool_result content blocks
func (m *Message) GetToolResults() []ContentBlock {
	var results []ContentBlock
	for _, block := range m.Content {
		if block.Type == ContentTypeToolResult {
			results = append(results, block)
		}
	}
	return results
}

// ParseTimestamp parses the entry timestamp
func (e *HistoryEntry) ParseTimestamp() (time.Time, error) {
	return time.Parse(time.RFC3339, e.Timestamp)
}

// TotalTokens returns the sum of input and output tokens
func (u *Usage) TotalTokens() int {
	return u.InputTokens + u.OutputTokens
}
