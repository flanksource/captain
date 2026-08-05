package claude

import (
	"github.com/segmentio/encoding/json"
	"strings"
	"time"
)

// HistoryEntry represents a single line in a JSONL transcript
type HistoryEntry struct {
	ParentUUID  string  `json:"parentUuid,omitempty"`
	SessionID   string  `json:"sessionId"`
	Version     string  `json:"version,omitempty"`
	CWD         string  `json:"cwd,omitempty"`
	GitBranch   string  `json:"gitBranch,omitempty"`
	Message     Message `json:"message"`
	UUID        string  `json:"uuid"`
	Timestamp   string  `json:"timestamp"`
	IsSidechain bool    `json:"isSidechain,omitempty"`
	AgentID     string  `json:"agentId,omitempty"`
	// Slug is Claude Code's session title slug. It doubles as the basename of the
	// exit-plan-mode plan file (~/.claude/plans/<slug>.md) when a plan exists.
	Slug string `json:"slug,omitempty"`
	// PlanFilePath is set on synthetic entries surfaced from plan_mode /
	// plan_mode_exit attachments; it points at the session's plan file even when
	// the transcript carries no ExitPlanMode tool call or plan-file write.
	PlanFilePath string           `json:"-"`
	Event        *TranscriptEvent `json:"-"`
	RawLine      json.RawMessage  `json:"-"`
	// Line is the 1-based JSONL line number the entry was read from, so
	// downstream consumers can seek back into the transcript file.
	Line int `json:"-"`
}

// TranscriptEvent is a non-message, non-tool transcript line. It lets callers
// preserve session/turn metadata without pretending discovery or budget records
// are model messages.
type TranscriptEvent struct {
	Type    string         `json:"type"`
	Scope   string         `json:"scope,omitempty"`
	Subtype string         `json:"subtype,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// Message represents a conversation message
type Message struct {
	// ID is the provider's message id (e.g. "msg_011Cdhc1..."), identifying one
	// API response. Claude Code writes a separate transcript line per content
	// block, all sharing this id and repeating the same Usage, so consumers that
	// aggregate usage must deduplicate on it.
	ID         string         `json:"id,omitempty"`
	Model      string         `json:"model,omitempty"`
	Role       MessageRole    `json:"role"`
	Content    []ContentBlock `json:"-"`
	StopReason StopReason     `json:"stop_reason,omitempty"`
	Usage      *Usage         `json:"usage,omitempty"`
}

// UnmarshalJSON handles polymorphic content field (string, array, or null)
func (m *Message) UnmarshalJSON(data []byte) error {
	type messageAlias struct {
		ID         string          `json:"id,omitempty"`
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

	m.ID = alias.ID
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
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
}

// Usage tracks token consumption
type Usage struct {
	InputTokens              int            `json:"input_tokens"`
	OutputTokens             int            `json:"output_tokens"`
	CacheCreationInputTokens int            `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int            `json:"cache_read_input_tokens,omitempty"`
	ServiceTier              string         `json:"service_tier,omitempty"`
	CacheCreation            *CacheCreation `json:"cache_creation,omitempty"`
	ServerToolUse            *ServerToolUse `json:"server_tool_use,omitempty"`
}

// CacheCreation contains detailed cache creation breakdown
type CacheCreation struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens,omitempty"`
}

// ServerToolUse tracks server-side tool usage
type ServerToolUse struct {
	WebSearchRequests int `json:"web_search_requests,omitempty"`
	WebFetchRequests  int `json:"web_fetch_requests,omitempty"`
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

// GetThinkingContent returns concatenated thinking text from all thinking blocks
func (m *Message) GetThinkingContent() string {
	var b strings.Builder
	for _, block := range m.Content {
		if block.Type == ContentTypeThinking && block.Thinking != "" {
			b.WriteString(block.Thinking)
		}
	}
	return b.String()
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
