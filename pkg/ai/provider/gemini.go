package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"google.golang.org/genai"
)

type Gemini struct {
	model      string
	apiKey     string
	httpClient *http.Client
}

func NewGemini(cfg ai.Config) *Gemini {
	model := cfg.Model
	if model == "" {
		model = "gemini-2.0-flash"
	}
	return &Gemini{model: model, apiKey: cfg.APIKey, httpClient: cfg.HTTPClient}
}

func (g *Gemini) GetModel() string      { return g.model }
func (g *Gemini) GetBackend() ai.Backend { return ai.BackendGemini }

func (g *Gemini) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()
	clientCfg := &genai.ClientConfig{
		APIKey:  g.apiKey,
		Backend: genai.BackendGeminiAPI,
	}
	if g.httpClient != nil {
		clientCfg.HTTPClient = g.httpClient
	}
	client, err := genai.NewClient(ctx, clientCfg)
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
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %v", ai.ErrTimeout, ctx.Err())
		}
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
