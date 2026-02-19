package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/flanksource/captain/pkg/ai"
)

type Anthropic struct {
	model  string
	apiKey string
}

func NewAnthropic(model, apiKey string) *Anthropic {
	if model == "" {
		model = "claude-sonnet-4"
	}
	return &Anthropic{model: model, apiKey: apiKey}
}

func (a *Anthropic) GetModel() string      { return a.model }
func (a *Anthropic) GetBackend() ai.Backend { return ai.BackendAnthropic }

func (a *Anthropic) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if a.apiKey == "" {
		return nil, ai.ErrNoAPIKey
	}

	start := time.Now()
	client := anthropic.NewClient(option.WithAPIKey(a.apiKey))

	maxTokens := int64(req.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(a.model),
		MaxTokens: maxTokens,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(req.Prompt)),
		},
	}

	if req.SystemPrompt != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.SystemPrompt}}
	}
	if req.Temperature > 0 {
		params.Temperature = anthropic.Float(req.Temperature)
	}

	if req.StructuredOutput != nil {
		schema, err := GenerateJSONSchema(req.StructuredOutput)
		if err != nil {
			return nil, fmt.Errorf("failed to generate schema: %w", err)
		}
		schemaJSON, err := SchemaToJSON(schema)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal schema: %w", err)
		}
		params.System = append(params.System, anthropic.TextBlockParam{
			Text: fmt.Sprintf("Respond with ONLY valid JSON matching this schema:\n%s", schemaJSON),
		})
	}

	msg, err := client.Messages.New(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("anthropic API error: %w", err)
	}

	var text string
	for _, block := range msg.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}

	var structuredData any
	if req.StructuredOutput != nil {
		cleaned := CleanupJSONResponse(text)
		if err := json.Unmarshal([]byte(cleaned), req.StructuredOutput); err != nil {
			return nil, fmt.Errorf("%w: %v", ai.ErrSchemaValidation, err)
		}
		structuredData = req.StructuredOutput
		text = ""
	}

	return &ai.Response{
		Text:           text,
		StructuredData: structuredData,
		Model:          string(msg.Model),
		Backend:        ai.BackendAnthropic,
		Usage: ai.Usage{
			InputTokens:      int(msg.Usage.InputTokens),
			OutputTokens:     int(msg.Usage.OutputTokens),
			CacheReadTokens:  int(msg.Usage.CacheReadInputTokens),
			CacheWriteTokens: int(msg.Usage.CacheCreationInputTokens),
		},
		Duration: time.Since(start),
		Raw:      msg,
	}, nil
}
