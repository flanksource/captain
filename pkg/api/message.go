package api

import (
	"encoding/json"
	"fmt"
	"strings"
)

// MessageRole identifies the author of one canonical conversation message.
type MessageRole string

const (
	RoleSystem    MessageRole = "system"
	RoleUser      MessageRole = "user"
	RoleAssistant MessageRole = "assistant"
	RoleTool      MessageRole = "tool"
)

func (r MessageRole) Valid() bool {
	switch r {
	case RoleSystem, RoleUser, RoleAssistant, RoleTool:
		return true
	default:
		return false
	}
}

// PartType identifies the provider-neutral content carried by a message part.
type PartType string

const (
	PartText        PartType = "text"
	PartReasoning   PartType = "reasoning"
	PartAttachment  PartType = "attachment"
	PartToolRequest PartType = "tool-request"
	PartToolResult  PartType = "tool-result"
)

func (t PartType) Valid() bool {
	switch t {
	case PartText, PartReasoning, PartAttachment, PartToolRequest, PartToolResult:
		return true
	default:
		return false
	}
}

// ToolRequest is one assistant request to execute a named tool. ToolCallID is
// stable across the corresponding ToolResult and provider projection.
type ToolRequest struct {
	ToolCallID string          `json:"toolCallId" yaml:"toolCallId"`
	Name       string          `json:"name" yaml:"name"`
	Input      json.RawMessage `json:"input,omitempty" yaml:"input,omitempty"`
}

// ToolResult is the output of a prior ToolRequest. Error is set instead of
// Output when tool execution failed.
type ToolResult struct {
	ToolCallID string          `json:"toolCallId" yaml:"toolCallId"`
	Output     json.RawMessage `json:"output,omitempty" yaml:"output,omitempty"`
	Error      string          `json:"error,omitempty" yaml:"error,omitempty"`
}

// Part is one typed content item in a canonical Message. Type selects exactly
// one payload: Text, Attachment, ToolRequest, or ToolResult.
type Part struct {
	Type        PartType       `json:"type" yaml:"type"`
	Text        string         `json:"text,omitempty" yaml:"text,omitempty"`
	Attachment  *AttachmentRef `json:"attachment,omitempty" yaml:"attachment,omitempty"`
	ToolRequest *ToolRequest   `json:"toolRequest,omitempty" yaml:"toolRequest,omitempty"`
	ToolResult  *ToolResult    `json:"toolResult,omitempty" yaml:"toolResult,omitempty"`
}

// Message is one provider-neutral conversation entry.
type Message struct {
	Role  MessageRole `json:"role" yaml:"role"`
	Parts []Part      `json:"parts" yaml:"parts"`
}

func (p Part) Validate() error {
	if !p.Type.Valid() {
		return fmt.Errorf("invalid part type %q", p.Type)
	}
	if err := p.validatePayloadShape(); err != nil {
		return err
	}
	switch p.Type {
	case PartText, PartReasoning:
		if strings.TrimSpace(p.Text) == "" {
			return fmt.Errorf("%s text is required", p.Type)
		}
	case PartAttachment:
		if err := p.Attachment.Validate(); err != nil {
			return fmt.Errorf("attachment: %w", err)
		}
	case PartToolRequest:
		if strings.TrimSpace(p.ToolRequest.ToolCallID) == "" {
			return fmt.Errorf("tool request call ID is required")
		}
		if strings.TrimSpace(p.ToolRequest.Name) == "" {
			return fmt.Errorf("tool request name is required")
		}
		if len(p.ToolRequest.Input) > 0 && !json.Valid(p.ToolRequest.Input) {
			return fmt.Errorf("tool request input must be valid JSON")
		}
	case PartToolResult:
		if strings.TrimSpace(p.ToolResult.ToolCallID) == "" {
			return fmt.Errorf("tool result call ID is required")
		}
		if len(p.ToolResult.Output) > 0 && !json.Valid(p.ToolResult.Output) {
			return fmt.Errorf("tool result output must be valid JSON")
		}
		if len(p.ToolResult.Output) > 0 && p.ToolResult.Error != "" {
			return fmt.Errorf("tool result output and error are mutually exclusive")
		}
	}
	return nil
}

func (p Part) validatePayloadShape() error {
	textSet := p.Text != ""
	attachmentSet := p.Attachment != nil
	requestSet := p.ToolRequest != nil
	resultSet := p.ToolResult != nil
	switch p.Type {
	case PartText, PartReasoning:
		if attachmentSet || requestSet || resultSet {
			return fmt.Errorf("%s part contains an incompatible payload", p.Type)
		}
	case PartAttachment:
		if !attachmentSet || textSet || requestSet || resultSet {
			return fmt.Errorf("attachment part must contain only an attachment")
		}
	case PartToolRequest:
		if !requestSet || textSet || attachmentSet || resultSet {
			return fmt.Errorf("tool-request part must contain only a tool request")
		}
	case PartToolResult:
		if !resultSet || textSet || attachmentSet || requestSet {
			return fmt.Errorf("tool-result part must contain only a tool result")
		}
	}
	return nil
}

// ValidateMessages validates role/part compatibility and tool transcript
// correlation. Tool results must directly follow the assistant message whose
// stable call IDs they resolve.
func ValidateMessages(messages []Message) error {
	if len(messages) == 0 {
		return fmt.Errorf("at least one message is required")
	}
	requestIDs := map[string]bool{}
	resultIDs := map[string]bool{}
	for i, message := range messages {
		if !message.Role.Valid() {
			return fmt.Errorf("message %d: invalid role %q", i+1, message.Role)
		}
		if len(message.Parts) == 0 {
			return fmt.Errorf("message %d (%s): at least one part is required", i+1, message.Role)
		}
		for j, part := range message.Parts {
			if err := part.Validate(); err != nil {
				return fmt.Errorf("message %d part %d: %w", i+1, j+1, err)
			}
			if !roleAllowsPart(message.Role, part.Type) {
				return fmt.Errorf("message %d part %d: %s message cannot contain %s", i+1, j+1, message.Role, part.Type)
			}
			if part.Type == PartToolRequest {
				if requestIDs[part.ToolRequest.ToolCallID] {
					return fmt.Errorf("message %d part %d: duplicate tool request call ID %q", i+1, j+1, part.ToolRequest.ToolCallID)
				}
				requestIDs[part.ToolRequest.ToolCallID] = true
			}
			if part.Type == PartToolResult {
				if resultIDs[part.ToolResult.ToolCallID] {
					return fmt.Errorf("message %d part %d: duplicate tool result call ID %q", i+1, j+1, part.ToolResult.ToolCallID)
				}
				resultIDs[part.ToolResult.ToolCallID] = true
			}
		}
		if message.Role == RoleTool {
			if err := validateToolMessage(messages, i); err != nil {
				return fmt.Errorf("message %d: %w", i+1, err)
			}
		}
	}
	return nil
}

func roleAllowsPart(role MessageRole, part PartType) bool {
	switch role {
	case RoleSystem:
		return part == PartText
	case RoleUser:
		return part == PartText || part == PartAttachment
	case RoleAssistant:
		return part == PartText || part == PartReasoning || part == PartToolRequest
	case RoleTool:
		return part == PartToolResult
	default:
		return false
	}
}

func validateToolMessage(messages []Message, index int) error {
	if index == 0 || messages[index-1].Role != RoleAssistant {
		return fmt.Errorf("tool message must immediately follow an assistant message")
	}
	preceding := map[string]bool{}
	for _, part := range messages[index-1].Parts {
		if part.Type == PartToolRequest && part.ToolRequest != nil {
			preceding[part.ToolRequest.ToolCallID] = true
		}
	}
	for _, part := range messages[index].Parts {
		if part.Type == PartToolResult && part.ToolResult != nil && !preceding[part.ToolResult.ToolCallID] {
			return fmt.Errorf("tool call %q was not requested by the preceding assistant message", part.ToolResult.ToolCallID)
		}
	}
	return nil
}
