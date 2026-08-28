// Package openai implements Captain's direct OpenAI API adapter. The OpenAI
// SDK stays behind Captain's provider-neutral contracts, and every model turn
// is sent through the Responses API with stateless input history.
package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"

	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
)

const maxToolTurns = 16

// Provider implements Captain's streaming API provider over OpenAI Responses.
type Provider struct {
	cfg    ai.Config
	client openaisdk.Client
	model  string
}

var _ ai.StreamingProvider = (*Provider)(nil)
var _ api.ToolCapableProvider = (*Provider)(nil)

// New constructs a direct OpenAI Responses provider.
func New(cfg ai.Config) (*Provider, error) {
	backend := cfg.Model.Backend
	if backend == "" {
		var err error
		backend, err = ai.InferBackend(cfg.Model.Name)
		if err != nil {
			return nil, err
		}
	}
	if backend != ai.BackendOpenAI {
		return nil, fmt.Errorf("openai provider does not support backend %q", backend)
	}
	if cfg.Model.Name == "" {
		return nil, fmt.Errorf("openai provider: model cannot be empty")
	}

	apiKey := cfg.APIKey
	if apiKey == "" {
		resolved, err := ai.ResolveAPIKey(backend)
		if err != nil {
			return nil, err
		}
		apiKey = resolved.Token
	}
	if apiKey == "" {
		return nil, fmt.Errorf("%w: openai provider has no API key (set OPENAI_API_KEY)", ai.ErrNoAPIKey)
	}

	cfg.Model.Backend = backend
	cfg.Model.Name = ai.NormalizeModelForBackend(backend, cfg.Model.Name)
	opts := []option.RequestOption{option.WithAPIKey(apiKey)}
	if cfg.APIURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.APIURL))
	}
	return &Provider{cfg: cfg, client: openaisdk.NewClient(opts...), model: cfg.Model.Name}, nil
}

func (p *Provider) GetModel() string       { return p.model }
func (p *Provider) GetBackend() ai.Backend { return ai.BackendOpenAI }

// SupportsCallerTools reports that Captain can expose and execute caller tools
// in-process for this provider.
func (p *Provider) SupportsCallerTools() bool { return true }

// Execute collects the streaming implementation into Captain's buffered
// response, binding structured JSON into the caller's Go target when present.
func (p *Provider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()
	events, err := p.ExecuteStream(ctx, req)
	if err != nil {
		return nil, err
	}

	var text strings.Builder
	var result *ai.Event
	for event := range events {
		switch event.Kind {
		case ai.EventText:
			text.WriteString(event.Text)
		case ai.EventError:
			if streamErr, ok := event.Raw.(error); ok {
				return nil, fmt.Errorf("openai responses: %w", streamErr)
			}
			return nil, fmt.Errorf("openai responses: %s", event.Error)
		case ai.EventResult:
			copy := event
			result = &copy
		}
	}
	if result == nil {
		return nil, fmt.Errorf("openai responses stream closed without a result")
	}

	out := &ai.Response{
		Text:         text.String(),
		Model:        p.model,
		Backend:      ai.BackendOpenAI,
		CostUSD:      result.CostUSD,
		Duration:     time.Since(start),
		Raw:          result.Raw,
		ToolApproval: result.ToolApproval,
	}
	if result.Usage != nil {
		out.Usage = *result.Usage
	}
	if len(result.StructuredData) > 0 {
		if req.Prompt.Schema != nil {
			if err := json.Unmarshal(result.StructuredData, req.Prompt.Schema); err != nil {
				return nil, fmt.Errorf("%w: %v", ai.ErrSchemaValidation, err)
			}
			out.StructuredData = req.Prompt.Schema
			out.Text = ""
		} else {
			out.StructuredData = result.StructuredData
			out.Text = string(result.StructuredData)
		}
	}
	return out, nil
}

// ExecuteStream starts a Responses API run and drives any model/function loop
// locally, emitting Captain's provider-neutral lifecycle events.
func (p *Provider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	prepared, err := p.prepare(req)
	if err != nil {
		return nil, err
	}

	out := make(chan ai.Event, 16)
	go func() {
		defer close(out)
		if err := p.run(ctx, req, prepared, out); err != nil {
			emit(ctx, out, ai.Event{Kind: ai.EventError, Error: err.Error(), Model: p.model, Raw: err})
		}
	}()
	return out, nil
}

func (p *Provider) run(ctx context.Context, req ai.Request, state *requestState, out chan<- ai.Event) error {
	var usage ai.Usage
	if state.resume != nil {
		outputs, err := p.resumeCalls(ctx, state.resume, state, out)
		if err != nil {
			return err
		}
		state.history = append(state.history, outputs...)
		state.resume = nil
	}
	for turn := 0; turn < maxToolTurns; turn++ {
		state.params.Input.OfInputItemList = state.history
		response, err := p.streamResponse(ctx, state.params, req.Prompt.HasSchema(), out)
		if err != nil {
			return err
		}
		usage = addUsage(usage, responseUsage(response.Usage))

		output, err := responseOutputParams(response.Output)
		if err != nil {
			return err
		}
		state.history = append(state.history, output...)
		calls := functionCalls(response.Output)
		if len(calls) == 0 {
			if refusal := responseRefusal(response.Output); refusal != "" {
				return fmt.Errorf("openai response refused: %s", refusal)
			}
			var structured json.RawMessage
			if req.Prompt.HasSchema() {
				structured = json.RawMessage(response.OutputText())
				if !json.Valid(structured) {
					return fmt.Errorf("%w: OpenAI returned invalid structured JSON", ai.ErrSchemaValidation)
				}
			}
			cost := ai.PriceUsage(ai.BackendOpenAI, p.model, usage, 0).Total()
			emit(ctx, out, ai.Event{
				Kind: ai.EventResult, Success: true, Usage: &usage, CostUSD: cost,
				Model: p.model, StructuredData: structured, Raw: response,
			})
			return nil
		}

		outputs, approval, err := p.resolveCalls(ctx, req, state, response, calls, out)
		if err != nil {
			return err
		}
		if approval != nil {
			cost := ai.PriceUsage(ai.BackendOpenAI, p.model, usage, 0).Total()
			emit(ctx, out, ai.Event{
				Kind: ai.EventResult, Success: true, Usage: &usage, CostUSD: cost,
				Model: p.model, ToolApproval: approval, Raw: response,
			})
			return nil
		}
		state.history = append(state.history, outputs...)
	}
	return fmt.Errorf("openai responses exceeded the %d-turn tool limit", maxToolTurns)
}

func (p *Provider) streamResponse(ctx context.Context, params responses.ResponseNewParams, structured bool, out chan<- ai.Event) (*responses.Response, error) {
	stream := p.client.Responses.NewStreaming(ctx, params)
	defer stream.Close()

	var completed *responses.Response
	for stream.Next() {
		event := stream.Current()
		switch value := event.AsAny().(type) {
		case responses.ResponseTextDeltaEvent:
			if !structured && value.Delta != "" {
				if !emit(ctx, out, ai.Event{Kind: ai.EventText, Text: value.Delta, Model: p.model}) {
					return nil, ctx.Err()
				}
			}
		case responses.ResponseReasoningSummaryTextDeltaEvent:
			if value.Delta != "" {
				if !emit(ctx, out, ai.Event{Kind: ai.EventThinking, Text: value.Delta, Model: p.model}) {
					return nil, ctx.Err()
				}
			}
		case responses.ResponseCompletedEvent:
			response := value.Response
			completed = &response
		case responses.ResponseFailedEvent:
			return nil, responseFailure(value.Response)
		case responses.ResponseIncompleteEvent:
			return nil, responseFailure(value.Response)
		case responses.ResponseErrorEvent:
			return nil, fmt.Errorf("OpenAI %s: %s", value.Code, value.Message)
		}
	}
	if err := stream.Err(); err != nil {
		return nil, p.normalizeError(ctx, err)
	}
	if completed == nil {
		return nil, fmt.Errorf("OpenAI Responses API stream closed before response.completed")
	}
	return completed, nil
}

func (p *Provider) normalizeError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %v", ai.ErrTimeout, ctx.Err())
	}
	return fmt.Errorf("OpenAI Responses API: %w", err)
}

func responseFailure(response responses.Response) error {
	if response.Error.Message != "" {
		return fmt.Errorf("OpenAI %s: %s", response.Error.Code, response.Error.Message)
	}
	if response.IncompleteDetails.Reason != "" {
		return fmt.Errorf("OpenAI response incomplete: %s", response.IncompleteDetails.Reason)
	}
	if response.Status != "" {
		return fmt.Errorf("OpenAI response ended with status %s", response.Status)
	}
	return fmt.Errorf("OpenAI response failed")
}

func responseUsage(value responses.ResponseUsage) ai.Usage {
	cached := int(value.InputTokensDetails.CachedTokens)
	reasoning := int(value.OutputTokensDetails.ReasoningTokens)
	return ai.Usage{
		InputTokens:     ai.NetInputTokens(int(value.InputTokens), cached),
		OutputTokens:    ai.NetOutputTokens(int(value.OutputTokens), reasoning),
		ReasoningTokens: reasoning,
		CacheReadTokens: cached,
	}
}

func addUsage(left, right ai.Usage) ai.Usage {
	return ai.Usage{
		InputTokens:      left.InputTokens + right.InputTokens,
		OutputTokens:     left.OutputTokens + right.OutputTokens,
		ReasoningTokens:  left.ReasoningTokens + right.ReasoningTokens,
		CacheReadTokens:  left.CacheReadTokens + right.CacheReadTokens,
		CacheWriteTokens: left.CacheWriteTokens + right.CacheWriteTokens,
	}
}

func emit(ctx context.Context, out chan<- ai.Event, event ai.Event) bool {
	select {
	case out <- event:
		return true
	case <-ctx.Done():
		return false
	}
}
