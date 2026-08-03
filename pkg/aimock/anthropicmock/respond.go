// ABOUTME: The `respond:` schema for the anthropic scenario section, and the wire types it renders into.
// ABOUTME: Shaped for the Messages API — thinking, tool_use and text blocks with a stop_reason.

package anthropicmock

import (
	"encoding/json"
	"fmt"
)

// Stop reasons the Messages API returns.
const (
	StopEndTurn   = "end_turn"
	StopToolUse   = "tool_use"
	StopMaxTokens = "max_tokens"
)

// Respond is one scripted assistant reply. Blocks are emitted in the order
// thinking → tool_use → text, matching how Claude orders them on the wire.
type Respond struct {
	Thinking  string   `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Signature string   `json:"signature,omitempty" yaml:"signature,omitempty"`
	Text      string   `json:"text,omitempty" yaml:"text,omitempty"`
	ToolUse   *ToolUse `json:"tool_use,omitempty" yaml:"tool_use,omitempty"`

	StopReason           string `json:"stop_reason,omitempty" yaml:"stop_reason,omitempty"`
	Usage                Usage  `json:"usage,omitempty" yaml:"usage,omitempty"`
	HoldOpenAfterContent bool   `json:"hold_open_after_content,omitempty" yaml:"hold_open_after_content,omitempty"`

	// Error, when set, makes this rule return an API error instead of a reply —
	// for exercising the retry and error-mapping paths.
	Error *Error `json:"error,omitempty" yaml:"error,omitempty"`
}

// ToolUse is a scripted tool call.
type ToolUse struct {
	Name  string         `json:"name" yaml:"name"`
	ID    string         `json:"id,omitempty" yaml:"id,omitempty"`
	Input map[string]any `json:"input,omitempty" yaml:"input,omitempty"`
}

// Usage is the token accounting attached to a reply.
type Usage struct {
	Input      int `json:"input,omitempty" yaml:"input,omitempty"`
	Output     int `json:"output,omitempty" yaml:"output,omitempty"`
	CacheRead  int `json:"cache_read,omitempty" yaml:"cache_read,omitempty"`
	CacheWrite int `json:"cache_write,omitempty" yaml:"cache_write,omitempty"`
}

// Error is a scripted API error.
type Error struct {
	Status  int    `json:"status,omitempty" yaml:"status,omitempty"`
	Type    string `json:"type,omitempty" yaml:"type,omitempty"`
	Message string `json:"message,omitempty" yaml:"message,omitempty"`
}

// resolvedStopReason infers the stop reason when the scenario omits it: a reply
// carrying a tool call stops for tool_use, anything else ends the turn.
func (r Respond) resolvedStopReason() string {
	if r.StopReason != "" {
		return r.StopReason
	}
	if r.ToolUse != nil {
		return StopToolUse
	}
	return StopEndTurn
}

// blocks renders the reply into ordered content blocks with their wire indices.
func (r Respond) blocks() []contentBlock {
	var out []contentBlock
	if r.Thinking != "" {
		signature := r.Signature
		if signature == "" {
			signature = "captain-mock-signature"
		}
		out = append(out, contentBlock{Type: "thinking", Thinking: r.Thinking, Signature: signature})
	}
	if r.ToolUse != nil {
		id := r.ToolUse.ID
		if id == "" {
			id = fmt.Sprintf("toolu_mock_%d", len(out))
		}
		input := r.ToolUse.Input
		if input == nil {
			input = map[string]any{}
		}
		raw, _ := json.Marshal(input)
		out = append(out, contentBlock{Type: "tool_use", ID: id, Name: r.ToolUse.Name, Input: raw})
	}
	if r.Text != "" {
		out = append(out, contentBlock{Type: "text", Text: r.Text})
	}
	if len(out) == 0 {
		out = append(out, contentBlock{Type: "text", Text: ""})
	}
	return out
}

// contentBlock is one block of an assistant message on the wire.
type contentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Signature string          `json:"signature,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

// wireUsage is the usage object the Messages API reports.
type wireUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (u Usage) wire() wireUsage {
	return wireUsage{
		InputTokens:              u.Input,
		OutputTokens:             u.Output,
		CacheReadInputTokens:     u.CacheRead,
		CacheCreationInputTokens: u.CacheWrite,
	}
}

// messageResponse is a complete non-streaming Messages API reply.
type messageResponse struct {
	ID           string         `json:"id"`
	Type         string         `json:"type"`
	Role         string         `json:"role"`
	Model        string         `json:"model"`
	Content      []contentBlock `json:"content"`
	StopReason   string         `json:"stop_reason"`
	StopSequence *string        `json:"stop_sequence"`
	Usage        wireUsage      `json:"usage"`
}

// errorResponse is the Messages API error envelope.
type errorResponse struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func newErrorResponse(errType, message string) errorResponse {
	var resp errorResponse
	resp.Type = "error"
	resp.Error.Type = errType
	resp.Error.Message = message
	return resp
}

// fallbackRespond is the bland reply served when strict mode is off and no rule
// matched. Strict mode (the default) returns aimock.MissStatus instead.
func fallbackRespond() *Respond {
	return &Respond{
		Text:       "captain-mock: unmatched request",
		StopReason: StopEndTurn,
		Usage:      Usage{Input: 1, Output: 1},
	}
}
