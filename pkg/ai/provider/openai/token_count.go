package openai

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/openai/openai-go/responses"
)

func (p *Provider) CountTokens(ctx context.Context, req api.Spec) (api.TokenCount, error) {
	if err := ctx.Err(); err != nil {
		return api.TokenCount{}, err
	}
	if err := req.ValidateRequestMode(); err != nil {
		return api.TokenCount{}, err
	}
	messages, excluded, err := tokenCountMessages(req)
	if err != nil {
		return api.TokenCount{}, err
	}
	prompt := req.Prompt
	prompt.User, prompt.System, prompt.AppendSystem = "", "", ""
	prompt.Attachments = nil
	countRequest := api.Spec{Prompt: prompt, Messages: messages, ToolPreferences: req.ToolPreferences, ToolPolicy: req.ToolPolicy}
	state, err := p.prepare(countRequest)
	if err != nil {
		return api.TokenCount{}, err
	}
	instructions := req.Prompt.System
	if req.Prompt.AppendSystem != "" {
		if instructions != "" {
			instructions += "\n"
		}
		instructions += req.Prompt.AppendSystem
	}
	body := struct {
		Model        string                            `json:"model"`
		Input        responses.ResponseInputParam      `json:"input"`
		Instructions string                            `json:"instructions,omitempty"`
		Tools        []responses.ToolUnionParam        `json:"tools,omitempty"`
		Text         responses.ResponseTextConfigParam `json:"text,omitempty"`
	}{Model: p.model, Input: state.history, Instructions: instructions, Tools: state.params.Tools, Text: state.params.Text}
	var result struct {
		InputTokens *int `json:"input_tokens"`
	}
	if err := p.client.Post(ctx, "responses/input_tokens", body, &result); err != nil {
		return api.TokenCount{}, fmt.Errorf("openai count tokens: %w", err)
	}
	if result.InputTokens == nil {
		return api.TokenCount{}, fmt.Errorf("openai count tokens: missing input_tokens")
	}
	return api.TokenCount{Tokens: *result.InputTokens, Excluded: excluded}, nil
}

func tokenCountMessages(req api.Spec) ([]api.Message, []string, error) {
	messages, err := ai.TokenMessages(req)
	if err != nil {
		return nil, nil, err
	}
	var available []api.Message
	var excluded []string
	for _, message := range messages {
		var parts []api.Part
		for _, part := range message.Parts {
			if part.Type == api.PartAttachment {
				if _, ok := part.Attachment.PreparedContent(); !ok {
					excluded = append(excluded, "unavailable attachment content")
					continue
				}
			}
			if part.Type == api.PartReasoning {
				excluded = append(excluded, "reasoning without encrypted replay data")
				continue
			}
			if part.Type == api.PartText && part.Text == "" {
				continue
			}
			parts = append(parts, part)
		}
		if len(parts) > 0 {
			message.Parts = parts
			available = append(available, message)
		}
	}
	if len(available) == 0 {
		return nil, nil, fmt.Errorf("openai count tokens: no available model content to count")
	}
	return available, excluded, nil
}

var _ api.TokenCountingProvider = (*Provider)(nil)
