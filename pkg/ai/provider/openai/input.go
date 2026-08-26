package openai

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	captools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/responses"
)

type requestState struct {
	params  responses.ResponseNewParams
	history responses.ResponseInputParam
	byName  map[string]api.ToolDefinition
	resume  *api.ToolApprovalResume
}

func (p *Provider) prepare(req ai.Request) (*requestState, error) {
	if err := req.ValidateRequestMode(); err != nil {
		return nil, err
	}
	if err := api.RequireToolPolicySupport(ai.BackendOpenAI, req.Permissions); err != nil {
		return nil, err
	}

	definitions, err := captools.ResolveDefinitions(p.cfg.Tools, captools.ResolveOptions{
		Preferences: req.ToolPreferences,
		Policy:      req.ToolPolicy,
	})
	if err != nil {
		return nil, err
	}

	state := &requestState{
		params: responses.ResponseNewParams{
			Model:   openaisdk.ResponsesModel(p.model),
			Store:   openaisdk.Bool(false),
			Include: []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		},
		byName: make(map[string]api.ToolDefinition, len(definitions)),
	}
	for _, definition := range definitions {
		state.byName[definition.Name] = definition
		tool := responses.ToolParamOfFunction(definition.Name, toolSchema(definition), definition.Strict != nil && *definition.Strict)
		if definition.Description != "" {
			tool.OfFunction.Description = openaisdk.String(definition.Description)
		}
		state.params.Tools = append(state.params.Tools, tool)
	}

	generation := ai.EffortConfig(ai.BackendOpenAI, p.model, req.Effort, req.Budget.MaxTokens, req.Temperature)
	if value, ok := generation["temperature"].(float64); ok {
		state.params.Temperature = openaisdk.Float(value)
	}
	if value, ok := generation["reasoning_effort"].(string); ok {
		state.params.Reasoning.Effort = responses.ReasoningEffort(value)
		state.params.Reasoning.Summary = responses.ReasoningSummaryAuto
	}
	if req.Budget.MaxTokens > 0 {
		state.params.MaxOutputTokens = openaisdk.Int(int64(req.Budget.MaxTokens))
	}
	if req.Prompt.HasSchema() {
		schema, err := ai.SchemaJSONForBackend(ai.BackendOpenAI, req.Prompt)
		if err != nil {
			return nil, fmt.Errorf("openai responses: cannot derive Prompt schema: %w", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(schema, &decoded); err != nil {
			return nil, fmt.Errorf("openai responses: invalid Prompt schema: %w", err)
		}
		format := responses.ResponseFormatTextConfigParamOfJSONSchema("captain_response", decoded)
		format.OfJSONSchema.Strict = openaisdk.Bool(true)
		state.params.Text.Format = format
	}

	if req.ToolApproval != nil {
		if err := validateMessageAttachments(p.cfg.Model, req.ToolApproval.State.Messages); err != nil {
			return nil, err
		}
		instructions, history, err := decodeApprovalCheckpoint(req.ToolApproval)
		if err != nil {
			return nil, err
		}
		state.params.Instructions = instructions
		state.history = history
		state.resume = req.ToolApproval
		return state, nil
	}

	if len(req.Messages) > 0 {
		if err := api.ValidateMessages(req.Messages); err != nil {
			return nil, fmt.Errorf("canonical messages: %w", err)
		}
		if err := validateMessageAttachments(p.cfg.Model, req.Messages); err != nil {
			return nil, err
		}
		state.history, err = conversationInput(req.Messages)
		if err != nil {
			return nil, err
		}
		return state, nil
	}

	if err := ai.ValidateAttachmentCompatibility([]api.Model{p.cfg.Model}, req.Prompt.Attachments); err != nil {
		return nil, err
	}
	if req.Prompt.System != "" {
		state.params.Instructions = openaisdk.String(req.Prompt.System)
	}
	content, err := promptContent(req.Prompt)
	if err != nil {
		return nil, err
	}
	if len(content) > 0 {
		state.history = append(state.history, messageInput(content, responses.EasyInputMessageRoleUser))
	}
	return state, nil
}

func toolSchema(definition api.ToolDefinition) map[string]any {
	if definition.InputSchema != nil {
		return definition.InputSchema
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

func promptContent(prompt api.Prompt) (responses.ResponseInputMessageContentListParam, error) {
	content := make(responses.ResponseInputMessageContentListParam, 0, len(prompt.Attachments))
	if prompt.User != "" {
		content = append(content, responses.ResponseInputContentParamOfInputText(prompt.User))
	}
	for i, attachment := range prompt.Attachments {
		part, err := attachmentContent(attachment, fmt.Sprintf("attachment %d", i+1))
		if err != nil {
			return nil, err
		}
		content = append(content, part)
	}
	return content, nil
}

func conversationInput(messages []api.Message) (responses.ResponseInputParam, error) {
	var input responses.ResponseInputParam
	for i, message := range messages {
		content := responses.ResponseInputMessageContentListParam{}
		var assistantText strings.Builder
		var calls []responses.ResponseInputItemUnionParam
		for j, part := range message.Parts {
			switch part.Type {
			case api.PartText:
				if message.Role == api.RoleAssistant {
					assistantText.WriteString(part.Text)
				} else {
					content = append(content, responses.ResponseInputContentParamOfInputText(part.Text))
				}
			case api.PartReasoning:
				// Provider summaries cannot be replayed as trusted reasoning without
				// the Responses API's encrypted reasoning item.
			case api.PartAttachment:
				item, err := attachmentContent(*part.Attachment, fmt.Sprintf("message %d part %d attachment", i+1, j+1))
				if err != nil {
					return nil, err
				}
				content = append(content, item)
			case api.PartToolRequest:
				calls = append(calls, responses.ResponseInputItemParamOfFunctionCall(
					string(part.ToolRequest.Input), part.ToolRequest.ToolCallID, part.ToolRequest.Name,
				))
			case api.PartToolResult:
				calls = append(calls, responses.ResponseInputItemParamOfFunctionCallOutput(
					part.ToolResult.ToolCallID, resultOutput(part.ToolResult),
				))
			}
		}
		if assistantText.Len() > 0 {
			input = append(input, messageInput(assistantText.String(), responses.EasyInputMessageRoleAssistant))
		}
		if len(content) > 0 {
			input = append(input, messageInput(content, responseRole(message.Role)))
		}
		input = append(input, calls...)
	}
	return input, nil
}

// messageInput writes the otherwise optional type discriminator because the
// durable approval checkpoint must be able to decode this SDK union later.
func messageInput[T string | responses.ResponseInputMessageContentListParam](content T, role responses.EasyInputMessageRole) responses.ResponseInputItemUnionParam {
	item := responses.ResponseInputItemParamOfMessage(content, role)
	item.OfMessage.Type = responses.EasyInputMessageTypeMessage
	return item
}

func responseRole(role api.MessageRole) responses.EasyInputMessageRole {
	switch role {
	case api.RoleSystem:
		return responses.EasyInputMessageRoleSystem
	case api.RoleAssistant:
		return responses.EasyInputMessageRoleAssistant
	default:
		return responses.EasyInputMessageRoleUser
	}
}

func attachmentContent(attachment api.AttachmentRef, label string) (responses.ResponseInputContentUnionParam, error) {
	content, ok := attachment.PreparedContent()
	if !ok {
		return responses.ResponseInputContentUnionParam{}, fmt.Errorf("%s (%s) is not prepared", label, attachment.ID)
	}
	data := content.Bytes
	if data == nil && content.Path != "" {
		var err error
		data, err = os.ReadFile(content.Path)
		if err != nil {
			return responses.ResponseInputContentUnionParam{}, fmt.Errorf("read prepared attachment %s: %w", attachment.ID, err)
		}
	}
	uri := "data:" + attachment.MediaType + ";base64," + base64.StdEncoding.EncodeToString(data)
	if strings.HasPrefix(attachment.MediaType, "image/") {
		image := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
		image.OfInputImage.ImageURL = openaisdk.String(uri)
		return image, nil
	}
	file := &responses.ResponseInputFileParam{FileData: openaisdk.String(uri)}
	if attachment.Filename != "" {
		file.Filename = openaisdk.String(attachment.Filename)
	}
	return responses.ResponseInputContentUnionParam{OfInputFile: file}, nil
}

func validateMessageAttachments(model api.Model, messages []api.Message) error {
	var attachments []api.AttachmentRef
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.Type == api.PartAttachment && part.Attachment != nil {
				attachments = append(attachments, *part.Attachment)
			}
		}
	}
	return ai.ValidateAttachmentCompatibility([]api.Model{model}, attachments)
}

func resultOutput(result *api.ToolResult) string {
	if result == nil {
		return "null"
	}
	if result.Error != "" {
		return jsonText(map[string]any{"error": result.Error})
	}
	return rawOutput(result.Output)
}

func rawOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "null"
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}
