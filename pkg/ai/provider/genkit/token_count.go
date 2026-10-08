package genkit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
	"google.golang.org/genai"
)

func (p *Provider) CountTokens(ctx context.Context, req api.Spec) (api.TokenCount, error) {
	if err := ctx.Err(); err != nil {
		return api.TokenCount{}, err
	}
	if p.provider != ai.Anthropic && p.provider != ai.Google {
		return api.TokenCount{}, fmt.Errorf("%w for %s", api.ErrTokenCountingUnsupported, p.provider.Name)
	}
	if err := req.ValidateRequestMode(); err != nil {
		return api.TokenCount{}, err
	}
	messages, err := ai.TokenMessages(req)
	if err != nil {
		return api.TokenCount{}, err
	}
	defs, err := tools.ResolveDefinitions(p.cfg.Tools, tools.ResolveOptions{Preferences: req.ToolPreferences, Policy: req.ToolPolicy})
	if err != nil {
		return api.TokenCount{}, err
	}
	if p.provider == ai.Anthropic {
		return p.countAnthropic(ctx, req, messages, defs)
	}
	return p.countGemini(ctx, req, messages, defs)
}

func (p *Provider) countAnthropic(ctx context.Context, req api.Spec, messages []api.Message, defs []api.ToolDefinition) (api.TokenCount, error) {
	body, excluded, err := anthropicCountBody(req, p.cfg.Model.Name, messages, defs)
	if err != nil {
		return api.TokenCount{}, err
	}
	data, err := json.Marshal(body)
	if err != nil {
		return api.TokenCount{}, err
	}
	var params anthropic.MessageCountTokensParams
	if err := json.Unmarshal(data, &params); err != nil {
		return api.TokenCount{}, fmt.Errorf("anthropic count parameters: %w", err)
	}
	opts := []option.RequestOption{option.WithAPIKey(p.cfg.APIKey), option.WithMaxRetries(0)}
	if p.cfg.APIURL != "" {
		opts = append(opts, option.WithBaseURL(p.cfg.APIURL))
	}
	client := anthropic.NewClient(opts...)
	count, err := client.Messages.CountTokens(ctx, params)
	if err != nil {
		return api.TokenCount{}, fmt.Errorf("anthropic count tokens: %w", err)
	}
	if !count.JSON.InputTokens.Valid() {
		return api.TokenCount{}, fmt.Errorf("anthropic count tokens: missing input_tokens")
	}
	return api.TokenCount{Tokens: int(count.InputTokens), Excluded: excluded}, nil
}

func (p *Provider) countGemini(ctx context.Context, req api.Spec, messages []api.Message, defs []api.ToolDefinition) (api.TokenCount, error) {
	if p.cfg.APIURL != "" {
		return api.TokenCount{}, fmt.Errorf("google provider does not support an API URL override")
	}
	body, excluded, err := geminiCountBody(req, p.cfg.Model.Name, messages, defs)
	if err != nil {
		return api.TokenCount{}, err
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: p.cfg.APIKey, Backend: genai.BackendGeminiAPI})
	if err != nil {
		return api.TokenCount{}, err
	}
	count, err := client.Models.CountTokens(ctx, p.cfg.Model.Name, nil, &genai.CountTokensConfig{HTTPOptions: &genai.HTTPOptions{
		ExtrasRequestProvider: func(map[string]any) map[string]any { return map[string]any{"generateContentRequest": body} },
	}})
	if err != nil {
		return api.TokenCount{}, fmt.Errorf("gemini count tokens: %w", err)
	}
	if count.TotalTokens <= 0 {
		return api.TokenCount{}, fmt.Errorf("gemini count tokens: missing or nonpositive totalTokens")
	}
	return api.TokenCount{Tokens: int(count.TotalTokens), Excluded: excluded}, nil
}

func tokenSystem(req api.Spec) string {
	return strings.Join(nonemptyTokenText(req.Prompt.System, req.Prompt.AppendSystem), "\n\n")
}
func nonemptyTokenText(values ...string) []string {
	var result []string
	for _, value := range values {
		if value != "" {
			result = append(result, value)
		}
	}
	return result
}

var _ api.TokenCountingProvider = (*Provider)(nil)
