package aichat

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

func agentPrompt(messages []api.Message, resumed bool) (string, []api.AttachmentRef, error) {
	selected := messages
	if resumed {
		index := lastUserMessage(messages)
		if index < 0 {
			return "", nil, fmt.Errorf("resumed agent chat requires a user message")
		}
		selected = messages[index : index+1]
	}
	blocks := make([]string, 0, len(selected))
	attachments := make([]api.AttachmentRef, 0)
	for _, message := range selected {
		text, refs, err := agentMessageText(message)
		if err != nil {
			return "", nil, err
		}
		attachments = append(attachments, refs...)
		if strings.TrimSpace(text) != "" {
			blocks = append(blocks, fmt.Sprintf("%s:\n%s", message.Role, text))
		}
	}
	if len(blocks) == 1 && len(selected) == 1 && selected[0].Role == api.RoleUser {
		return strings.TrimPrefix(blocks[0], string(api.RoleUser)+":\n"), attachments, nil
	}
	return strings.Join(blocks, "\n\n"), attachments, nil
}

func lastUserMessage(messages []api.Message) int {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == api.RoleUser {
			return i
		}
	}
	return -1
}

func agentMessageText(message api.Message) (string, []api.AttachmentRef, error) {
	lines := make([]string, 0, len(message.Parts))
	attachments := make([]api.AttachmentRef, 0)
	for _, part := range message.Parts {
		switch part.Type {
		case api.PartText:
			lines = append(lines, part.Text)
		case api.PartReasoning:
			continue
		case api.PartAttachment:
			attachments = append(attachments, *part.Attachment)
			lines = append(lines, "[Attachment: "+part.Attachment.Filename+"]")
		case api.PartToolRequest:
			lines = append(lines, fmt.Sprintf("Tool request %s (%s): %s",
				part.ToolRequest.Name, part.ToolRequest.ToolCallID, jsonText(part.ToolRequest.Input)))
		case api.PartToolResult:
			if part.ToolResult.Error != "" {
				lines = append(lines, fmt.Sprintf("Tool result %s failed: %s", part.ToolResult.ToolCallID, part.ToolResult.Error))
			} else {
				lines = append(lines, fmt.Sprintf("Tool result %s: %s",
					part.ToolResult.ToolCallID, jsonText(part.ToolResult.Output)))
			}
		default:
			return "", nil, fmt.Errorf("unsupported agent prompt part %q", part.Type)
		}
	}
	return strings.Join(lines, "\n"), attachments, nil
}

func jsonText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "{}"
	}
	return string(raw)
}
