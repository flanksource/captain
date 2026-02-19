package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"google.golang.org/genai"
)

type Gemini struct {
	model  string
	apiKey string
}

func NewGemini(model, apiKey string) *Gemini {
	if model == "" {
		model = "gemini-2.0-flash"
	}
	return &Gemini{model: model, apiKey: apiKey}
}

func (g *Gemini) GetModel() string      { return g.model }
func (g *Gemini) GetBackend() ai.Backend { return ai.BackendGemini }

func (g *Gemini) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if g.apiKey == "" {
		return nil, ai.ErrNoAPIKey
	}

	start := time.Now()
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:  g.apiKey,
		Backend: genai.BackendGeminiAPI,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create gemini client: %w", err)
	}

	config := &genai.GenerateContentConfig{}

	if req.SystemPrompt != "" {
		config.SystemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: req.SystemPrompt}},
		}
	}
	if req.MaxTokens > 0 {
		config.MaxOutputTokens = int32(req.MaxTokens)
	}
	if req.Temperature > 0 {
		t := float32(req.Temperature)
		config.Temperature = &t
	}

	if req.StructuredOutput != nil {
		schema, err := GenerateJSONSchema(req.StructuredOutput)
		if err != nil {
			return nil, fmt.Errorf("failed to generate schema: %w", err)
		}
		config.ResponseMIMEType = "application/json"
		config.ResponseJsonSchema = schema
	}

	resp, err := client.Models.GenerateContent(ctx, g.model, genai.Text(req.Prompt), config)
	if err != nil {
		return nil, fmt.Errorf("gemini API error: %w", err)
	}

	text := resp.Text()

	var structuredData any
	if req.StructuredOutput != nil {
		cleaned := CleanupJSONResponse(text)
		if err := json.Unmarshal([]byte(cleaned), req.StructuredOutput); err != nil {
			return nil, fmt.Errorf("%w: %v", ai.ErrSchemaValidation, err)
		}
		structuredData = req.StructuredOutput
		text = ""
	}

	usage := ai.Usage{}
	if resp.UsageMetadata != nil {
		usage = ai.Usage{
			InputTokens:     int(resp.UsageMetadata.PromptTokenCount),
			OutputTokens:    int(resp.UsageMetadata.CandidatesTokenCount),
			CacheReadTokens: int(resp.UsageMetadata.CachedContentTokenCount),
		}
	}

	return &ai.Response{
		Text:           text,
		StructuredData: structuredData,
		Model:          g.model,
		Backend:        ai.BackendGemini,
		Usage:          usage,
		Duration:       time.Since(start),
		Raw:            resp,
	}, nil
}
