package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/shared"
)

type OpenAI struct {
	model      string
	apiKey     string
	httpClient *http.Client
}

func NewOpenAI(cfg ai.Config) *OpenAI {
	model := cfg.Model
	if model == "" {
		model = "gpt-4o"
	}
	return &OpenAI{model: model, apiKey: cfg.APIKey, httpClient: cfg.HTTPClient}
}

func (o *OpenAI) GetModel() string       { return o.model }
func (o *OpenAI) GetBackend() ai.Backend { return ai.BackendOpenAI }

func (o *OpenAI) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()

	var opts []option.RequestOption
	if o.apiKey != "" {
		opts = append(opts, option.WithAPIKey(o.apiKey))
	}
	if o.httpClient != nil {
		opts = append(opts, option.WithHTTPClient(o.httpClient))
	}
	client := openai.NewClient(opts...)

	var messages []openai.ChatCompletionMessageParamUnion
	if req.SystemPrompt != "" {
		messages = append(messages, openai.ChatCompletionMessageParamUnion{
			OfSystem: &openai.ChatCompletionSystemMessageParam{
				Content: openai.ChatCompletionSystemMessageParamContentUnion{
					OfString: openai.String(req.SystemPrompt),
				},
			},
		})
	}
	messages = append(messages, openai.ChatCompletionMessageParamUnion{
		OfUser: &openai.ChatCompletionUserMessageParam{
			Content: openai.ChatCompletionUserMessageParamContentUnion{
				OfString: openai.String(req.Prompt),
			},
		},
	})

	params := openai.ChatCompletionNewParams{
		Model:       shared.ChatModel(o.model),
		Messages:    messages,
		Temperature: openai.Float(0.0),
	}

	if req.StructuredOutput != nil {
		schema, err := GenerateJSONSchema(req.StructuredOutput)
		if err != nil {
			return nil, fmt.Errorf("failed to generate schema: %w", err)
		}
		schemaBytes, err := json.Marshal(schema)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal schema: %w", err)
		}
		var schemaInterface interface{}
		if err := json.Unmarshal(schemaBytes, &schemaInterface); err != nil {
			return nil, fmt.Errorf("failed to unmarshal schema: %w", err)
		}
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:        "response",
					Description: openai.String("Structured response"),
					Schema:      schemaInterface,
					Strict:      openai.Bool(true),
				},
			},
		}
	}

	if req.MaxTokens > 0 {
		params.MaxTokens = openai.Int(int64(req.MaxTokens))
	}
	if req.Temperature > 0 {
		params.Temperature = openai.Float(req.Temperature)
	}

	resp, err := client.Chat.Completions.New(ctx, params)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %v", ai.ErrTimeout, ctx.Err())
		}
		return nil, fmt.Errorf("openai API error: %w", err)
	}

	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("no response from OpenAI")
	}

	text := resp.Choices[0].Message.Content

	var structuredData any
	if req.StructuredOutput != nil {
		cleaned := CleanupJSONResponse(text)
		if err := json.Unmarshal([]byte(cleaned), req.StructuredOutput); err != nil {
			return nil, fmt.Errorf("%w: %v", ai.ErrSchemaValidation, err)
		}
		structuredData = req.StructuredOutput
		text = ""
	}

	usage := ai.Usage{
		InputTokens:  int(resp.Usage.PromptTokens),
		OutputTokens: int(resp.Usage.CompletionTokens),
	}
	if resp.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
		usage.ReasoningTokens = int(resp.Usage.CompletionTokensDetails.ReasoningTokens)
	}

	return &ai.Response{
		Text:           text,
		StructuredData: structuredData,
		Model:          resp.Model,
		Backend:        ai.BackendOpenAI,
		Usage:          usage,
		Duration:       time.Since(start),
		Raw:            resp,
	}, nil
}
