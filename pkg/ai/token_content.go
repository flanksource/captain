package ai

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
)

// TokenMessages projects the authored content only; it never runs setup or tools.
func TokenMessages(req api.Spec) ([]api.Message, error) {
	if req.ToolApproval != nil {
		return nil, fmt.Errorf("token sizing does not accept tool approval checkpoints")
	}
	if len(req.Messages) > 0 {
		if err := api.ValidateMessages(req.Messages); err != nil {
			return nil, err
		}
		return req.Messages, nil
	}
	if err := req.Prompt.Validate(); err != nil {
		return nil, err
	}
	parts := []api.Part{{Type: api.PartText, Text: req.Prompt.User}}
	for _, ref := range req.Prompt.Attachments {
		ref := ref
		parts = append(parts, api.Part{Type: api.PartAttachment, Attachment: &ref})
	}
	return []api.Message{{Role: api.RoleUser, Parts: parts}}, nil
}

func estimateTokenRequest(req api.Spec, cfg api.Config, model api.Model) (api.TokenCount, error) {
	messages, err := TokenMessages(req)
	if err != nil {
		return api.TokenCount{}, err
	}
	count := api.TokenCount{Tokens: estimateTokenText(req.Prompt.System) + estimateTokenText(req.Prompt.AppendSystem)}
	for _, msg := range messages {
		for _, part := range msg.Parts {
			switch part.Type {
			case api.PartText, api.PartReasoning:
				count.Tokens += estimateTokenText(part.Text)
			case api.PartToolRequest:
				data, err := json.Marshal(part.ToolRequest)
				if err != nil {
					return api.TokenCount{}, err
				}
				count.Tokens += estimateTokenText(string(data))
			case api.PartToolResult:
				data, err := json.Marshal(part.ToolResult)
				if err != nil {
					return api.TokenCount{}, err
				}
				count.Tokens += estimateTokenText(string(data))
			case api.PartAttachment:
				count.Excluded = append(count.Excluded, "attachments")
			default:
				return api.TokenCount{}, fmt.Errorf("cannot estimate part type %q", part.Type)
			}
		}
	}
	definitions, err := tools.ResolveDefinitions(cfg.Tools, tools.ResolveOptions{Preferences: req.ToolPreferences, Policy: req.ToolPolicy})
	if err != nil {
		return api.TokenCount{}, err
	}
	for _, def := range definitions {
		data, err := json.Marshal(struct {
			Name        string         `json:"name"`
			Description string         `json:"description"`
			Schema      map[string]any `json:"input_schema"`
		}{def.Name, def.Description, def.InputSchema})
		if err != nil {
			return api.TokenCount{}, err
		}
		count.Tokens += estimateTokenText(string(data))
	}
	if req.Prompt.HasSchema() {
		schema, err := SchemaJSONForRuntime(model.Provider, model.Mode, req.Prompt)
		if err != nil {
			return api.TokenCount{}, err
		}
		count.Tokens += estimateTokenText(string(schema))
	}
	return count, nil
}

func estimateTokenText(text string) int { return (len(text) + 3) / 4 }
