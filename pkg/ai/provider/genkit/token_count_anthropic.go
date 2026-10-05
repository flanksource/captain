package genkit

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

type tokenToolDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}
type anthropicTokenMessage struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}
type anthropicTokenBody struct {
	Model        string                  `json:"model"`
	Messages     []anthropicTokenMessage `json:"messages"`
	System       string                  `json:"system,omitempty"`
	Tools        []tokenToolDefinition   `json:"tools,omitempty"`
	OutputConfig map[string]any          `json:"output_config,omitempty"`
}

func anthropicCountBody(req api.Spec, model string, messages []api.Message, defs []api.ToolDefinition) (anthropicTokenBody, []string, error) {
	body := anthropicTokenBody{Model: model, System: tokenSystem(req)}
	var excluded []string
	for _, msg := range messages {
		if msg.Role == api.RoleSystem {
			for _, part := range msg.Parts {
				body.System = strings.Join(nonemptyTokenText(body.System, part.Text), "\n\n")
			}
			continue
		}
		role := "user"
		if msg.Role == api.RoleAssistant {
			role = "assistant"
		}
		native := anthropicTokenMessage{Role: role}
		for _, part := range msg.Parts {
			block, missing, err := anthropicTokenPart(part)
			if err != nil {
				return body, nil, err
			}
			if missing {
				excluded = append(excluded, "unavailable attachment content")
			}
			if block != nil {
				native.Content = append(native.Content, block)
			}
			if part.Type == api.PartReasoning {
				excluded = append(excluded, "reasoning signatures and encrypted reasoning")
			}
		}
		if len(native.Content) > 0 {
			body.Messages = append(body.Messages, native)
		}
	}
	if len(body.Messages) == 0 {
		return body, nil, fmt.Errorf("anthropic count tokens: no countable content")
	}
	for _, def := range defs {
		body.Tools = append(body.Tools, tokenToolDefinition{Name: def.Name, Description: def.Description, InputSchema: api.ObjectSchema(def.InputSchema)})
	}
	if req.Prompt.HasSchema() {
		schema, err := ai.SchemaJSONForRuntime(ai.Anthropic, ai.ModeAPI, req.Prompt)
		if err != nil {
			return body, nil, err
		}
		body.OutputConfig = map[string]any{"format": map[string]any{"type": "json_schema", "schema": json.RawMessage(schema)}}
	}
	return body, excluded, nil
}

func anthropicTokenPart(part api.Part) (map[string]any, bool, error) {
	switch part.Type {
	case api.PartText, api.PartReasoning:
		if part.Text == "" {
			return nil, false, nil
		}
		return map[string]any{"type": "text", "text": part.Text}, false, nil
	case api.PartToolRequest:
		input := part.ToolRequest.Input
		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		return map[string]any{"type": "tool_use", "id": part.ToolRequest.ToolCallID, "name": part.ToolRequest.Name, "input": input}, false, nil
	case api.PartToolResult:
		return map[string]any{"type": "tool_result", "tool_use_id": part.ToolResult.ToolCallID, "content": strings.Join(nonemptyTokenText(string(part.ToolResult.Output), part.ToolResult.Error), "\n"), "is_error": part.ToolResult.Error != ""}, false, nil
	case api.PartAttachment:
		data, err := tokenAttachmentBytes(*part.Attachment)
		if err != nil {
			return nil, false, err
		}
		if data == nil {
			return nil, true, nil
		}
		kind := "image"
		if part.Attachment.MediaType == "application/pdf" {
			kind = "document"
		} else if !strings.HasPrefix(part.Attachment.MediaType, "image/") {
			return nil, false, fmt.Errorf("anthropic count tokens: unsupported attachment %q", part.Attachment.MediaType)
		}
		return map[string]any{"type": kind, "source": map[string]any{"type": "base64", "media_type": part.Attachment.MediaType, "data": base64.StdEncoding.EncodeToString(data)}}, false, nil
	default:
		return nil, false, fmt.Errorf("cannot count part type %q", part.Type)
	}
}

func tokenAttachmentBytes(ref api.AttachmentRef) ([]byte, error) {
	content, ok := ref.PreparedContent()
	if !ok {
		return nil, nil
	}
	if len(content.Bytes) > 0 {
		return content.Bytes, nil
	}
	if content.Path != "" {
		return os.ReadFile(content.Path)
	}
	return nil, fmt.Errorf("prepared attachment %q has no content", ref.Filename)
}
