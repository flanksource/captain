// Package genkit implements Captain's Genkit-backed API providers. The runtime
// registry uses it for Anthropic, Gemini, and DeepSeek; OpenAI compatibility is
// retained for direct consumers while Captain's OpenAI runtime uses its official
// SDK and Responses API adapter.
package genkit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/logger"

	gkai "github.com/firebase/genkit/go/ai"
	gk "github.com/firebase/genkit/go/genkit"
)

// log is the package-scoped logger for AI providers. Its level follows
// -v/--log-level and can be tuned with -Plog.level.ai=debug.
var log = logger.GetLogger("ai")

// Provider is a genkit-backed ai.StreamingProvider for one provider's API mode.
type Provider struct {
	cfg             ai.Config
	provider        *ai.ModelProvider
	g               *gk.Genkit
	modelRef        string
	toolOptionsMu   sync.Mutex
	toolCorrelation *toolEventCorrelation
}

var _ ai.StreamingProvider = (*Provider)(nil)

// New builds a genkit provider from an already-resolved model. It fails loud on
// an unresolved model, a family genkit does not serve, a mode other than api, or
// a missing API key.
func New(cfg ai.Config) (*Provider, error) {
	// The provider is read off the resolved model, never re-derived: this factory
	// runs after api.NewProvider has resolved cfg.Model, and a second derivation
	// here could disagree with the one already recorded.
	provider := cfg.Model.Provider
	if provider == nil {
		return nil, fmt.Errorf("genkit provider needs a resolved model: %q names no provider", cfg.Model.Name)
	}

	switch provider {
	case ai.Anthropic, ai.OpenAI, ai.Google, ai.DeepSeek:
	default:
		return nil, fmt.Errorf("genkit provider does not support %q (supported: %s)", provider.Name, ai.ProviderList())
	}
	// genkit is the api mode. GetRuntime says so, so a model recording another
	// mode would make this adapter misreport the runtime it ran on.
	if cfg.Model.Mode != "" && cfg.Model.Mode != ai.ModeAPI {
		return nil, fmt.Errorf("genkit provider does not support %s mode (it serves %s api)", cfg.Model.Mode, provider.Name)
	}

	apiKey := cfg.APIKey
	if apiKey == "" {
		resolved, err := ai.ResolveAPIKey(provider, ai.ModeAPI)
		if err != nil {
			return nil, err
		}
		apiKey = resolved.Token
	}
	if apiKey == "" {
		return nil, fmt.Errorf("%w: genkit provider has no API key for %q (set %s)", ai.ErrNoAPIKey, provider.Name, strings.Join(ai.AuthEnvVars(provider, ai.ModeAPI), " or "))
	}

	ref, err := modelRef(provider, cfg.Model.Name)
	if err != nil {
		return nil, err
	}

	g, err := getInstance(context.Background(), provider, apiKey, cfg.APIURL)
	if err != nil {
		return nil, err
	}

	cfg.APIKey = apiKey
	return &Provider{cfg: cfg, provider: provider, g: g, modelRef: ref}, nil
}

func (p *Provider) GetModel() string { return p.cfg.Model.Name }

// GetRuntime reports the (provider, mode) pair this adapter serves. genkit is
// the API mode for every family it supports.
func (p *Provider) GetRuntime() ai.Runtime { return ai.RuntimeOf(p.provider, ai.ModeAPI) }

// Execute returns one complete generation, streaming internally for Anthropic.
func (p *Provider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()

	var stream gkai.ModelStreamCallback
	if p.provider == ai.Anthropic {
		stream = func(ctx context.Context, _ *gkai.ModelResponseChunk) error { return ctx.Err() }
	}
	turns := newTurnLog(p.provider, p.cfg.Model.Name, nil)
	opts, err := p.correlatedGenerateOptions(req, stream, nil, nil, turns)
	if err != nil {
		return nil, err
	}
	resp, err := p.generate(ctx, opts)
	if err != nil {
		err = p.generationError(ctx, resp, err)
		// A rejected answer rides back with the error, so schema repair can show
		// the model what it got wrong.
		if rejected := rejectedAnswer(resp, err); rejected != "" {
			return &ai.Response{Text: rejected, Model: p.cfg.Model.Name, Runtime: ai.RuntimeOf(p.provider, ai.ModeAPI)}, err
		}
		return nil, err
	}

	out := responseToResponse(ctx, resp, turns.total(), p.provider, p.cfg.Model.Name, start)
	if resp.FinishReason == gkai.FinishReasonInterrupted {
		out.ToolApproval, err = toolApprovalState(req, resp)
		if err != nil {
			return nil, err
		}
	}

	if out.ToolApproval != nil {
		return out, nil
	}
	if err := unfinishedReply(req, resp); err != nil {
		return nil, err
	}
	structured, _, err := structuredResponseData(req, resp)
	if err != nil {
		return nil, err
	}
	if structured != nil {
		out.StructuredData = structured
	}
	if req.Prompt.Schema != nil {
		out.Text = ""
	}

	if cost := p.costUSD(out.Usage); cost > 0 {
		out.CostUSD = cost
		log.Debugf("genkit %s cost: $%.6f (model=%s)", p.provider.Name, cost, p.cfg.Model.Name)
	}

	return out, nil
}

// generationError classifies a failed generation. genkit hands the model's reply
// back with the error when the model finished but its output did not parse, and
// how the reply finished says more than the parse error does: an answer cut off
// at max_tokens is unfinished, not wrong.
func (p *Provider) generationError(ctx context.Context, resp *gkai.ModelResponse, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("%w: %v", ai.ErrTimeout, ctx.Err())
	}
	if resp != nil && resp.FinishReason == gkai.FinishReasonLength {
		return truncatedError(err)
	}
	// Genkit validates the model's constrained output against the request
	// schema during generation. Preserve the details but classify the rejection
	// so schema-validation middleware can act on buffered calls and streamed
	// callers receive the same failure class.
	if isSchemaMismatch(err) {
		return fmt.Errorf("%w: %v", ai.ErrSchemaValidation, err)
	}
	return fmt.Errorf("genkit %s generate: %w", p.provider.Name, err)
}

func truncatedError(cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: the model reached max_tokens before finishing its answer", ai.ErrOutputTruncated)
	}
	return fmt.Errorf("%w: the model reached max_tokens before finishing its answer (%v)", ai.ErrOutputTruncated, cause)
}

// unfinishedReply refuses a reply that cannot hold a structured answer because
// the model did not finish it: genkit passes such a reply through unparsed so
// its finish reason is not masked by a schema error.
func unfinishedReply(req ai.Request, resp *gkai.ModelResponse) error {
	if !req.Prompt.HasSchema() {
		return nil
	}
	switch resp.FinishReason {
	case gkai.FinishReasonLength:
		return truncatedError(nil)
	case gkai.FinishReasonBlocked, gkai.FinishReasonFailed, gkai.FinishReasonAborted, gkai.FinishReasonOther:
		return fmt.Errorf("model finished with %q before giving a structured answer: %s", resp.FinishReason, resp.FinishMessage)
	default:
		return nil
	}
}

// rejectedAnswer is the reply genkit rejected as not matching the schema.
func rejectedAnswer(resp *gkai.ModelResponse, err error) string {
	if resp == nil || resp.Message == nil || !errors.Is(err, ai.ErrSchemaValidation) {
		return ""
	}
	return resp.Message.Text()
}

func structuredResponseData(req ai.Request, resp *gkai.ModelResponse) (any, json.RawMessage, error) {
	if req.Prompt.Schema != nil {
		if err := resp.Output(req.Prompt.Schema); err != nil {
			return nil, nil, fmt.Errorf("%w: %v", ai.ErrSchemaValidation, err)
		}
		raw, err := json.Marshal(req.Prompt.Schema)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: encode structured output: %v", ai.ErrSchemaValidation, err)
		}
		return req.Prompt.Schema, raw, nil
	}
	if len(req.Prompt.SchemaJSON) == 0 {
		return nil, nil, nil
	}
	var value any
	if err := resp.Output(&value); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ai.ErrSchemaValidation, err)
	}
	raw, err := structuredJSONBytes(value)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: encode structured output: %v", ai.ErrSchemaValidation, err)
	}
	return raw, raw, nil
}

func structuredJSONBytes(value any) (json.RawMessage, error) {
	if data, ok := value.([]byte); ok {
		var raw json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		return raw, nil
	}
	return json.Marshal(value)
}

// isSchemaMismatch reports whether a genkit Generate error is the library's
// constrained-output validation failure (vs a transport/model error), so the
// middleware can re-ask the model with the errors instead of failing.
func isSchemaMismatch(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "did not match expected schema") ||
		strings.Contains(msg, "output matching expected schema")
}

// ExecuteStream runs a streaming generation, publishing each chunk as ai.Events
// and a terminal EventResult carrying usage + best-effort cost. Structured JSON
// chunks stay internal; their validated final value is carried by EventResult.
func (p *Provider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	ch := make(chan ai.Event, 16)
	correlation := newToolEventCorrelation()
	cb := func(_ context.Context, chunk *gkai.ModelResponseChunk) error {
		events, err := chunkToEvents(chunk, p.cfg.Model.Name, correlation)
		if err != nil {
			return err
		}
		for _, ev := range events {
			if req.Prompt.HasSchema() && ev.Kind == ai.EventText {
				continue
			}
			select {
			case ch <- ev:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	// emit lets in-process caller-tool execution publish tool_use/permission/
	// tool_result events onto the same stream as the model's text chunks.
	emit := func(ev ai.Event) {
		select {
		case ch <- ev:
		case <-ctx.Done():
		}
	}

	turns := newTurnLog(p.provider, p.cfg.Model.Name, emit)
	opts, err := p.correlatedGenerateOptions(req, cb, emit, correlation, turns)
	if err != nil {
		return nil, err
	}
	go func() {
		defer close(ch)
		resp, err := p.generate(ctx, opts)
		usage := turns.total()
		costUSD := 0.0
		if usage != nil {
			costUSD = p.costUSD(*usage)
		}
		// Every failure keeps what the generation cost; a rejected answer rides
		// along as Text for schema repair.
		fail := func(err error) {
			ch <- ai.Event{Kind: ai.EventError, Error: err.Error(), Text: rejectedAnswer(resp, err), Model: p.cfg.Model.Name, Usage: usage, CostUSD: costUSD}
		}
		if err != nil {
			fail(p.generationError(ctx, resp, err))
			return
		}
		var approval *api.ToolApprovalState
		if resp.FinishReason == gkai.FinishReasonInterrupted {
			approval, err = toolApprovalState(req, resp)
			if err != nil {
				fail(err)
				return
			}
		}
		var structured json.RawMessage
		if approval == nil {
			if err := unfinishedReply(req, resp); err != nil {
				fail(err)
				return
			}
			_, structured, err = structuredResponseData(req, resp)
			if err != nil {
				fail(err)
				return
			}
		}
		ch <- ai.Event{
			Kind:           ai.EventResult,
			Success:        true,
			Usage:          usage,
			CostUSD:        costUSD,
			Model:          p.cfg.Model.Name,
			ToolApproval:   approval,
			StructuredData: structured,
		}
	}()

	return ch, nil
}

// generate runs one generation on a context that ends with the call. Genkit's
// Anthropic plugin never closes the stream NewStreaming opens and returns at
// message_stop before the body reaches EOF, so ending the request context is
// what releases the response body and its connection. Callers keep the
// outer ctx to classify errors, which this cancellation must not affect.
func (p *Provider) generate(ctx context.Context, opts []gkai.GenerateOption) (*gkai.ModelResponse, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	return gk.Generate(ctx, p.g, opts...)
}

func (p *Provider) correlatedGenerateOptions(
	req ai.Request,
	stream gkai.ModelStreamCallback,
	emit func(ai.Event),
	correlation *toolEventCorrelation,
	turns *turnLog,
) ([]gkai.GenerateOption, error) {
	// Caller tools are gated by ToolPreferences and OnApproval; Permissions.Tools
	// is a separate policy the API mode has no seam for, so it must not be
	// accepted and ignored.
	if err := api.RequireToolPolicySupport(p.provider, ai.ModeAPI, req.Permissions); err != nil {
		return nil, err
	}
	p.toolOptionsMu.Lock()
	defer p.toolOptionsMu.Unlock()
	p.toolCorrelation = correlation
	defer func() { p.toolCorrelation = nil }()
	if err := seedToolApprovalCorrelation(req.ToolApproval, correlation); err != nil {
		return nil, err
	}
	return generateOptions(p, req, stream, emit, turns)
}

// pricingModelID is the OpenRouter pricing key for a provider+model. It uses the
// provider's PricingPrefix — "google" for Gemini, whose catalog namespace is
// "googleai" (see modelRef). The two must not be derived from one another.
func pricingModelID(provider *ai.ModelProvider, model string) string {
	if provider == nil {
		return model
	}
	return provider.PricingIDs(model)[0]
}

// costUSD prices a generation. Genkit usage carries no cost, so it is computed
// from the pricing registry — best effort: an unpriced model logs at debug and
// returns 0 rather than failing the request.
func (p *Provider) costUSD(u ai.Usage) float64 {
	id := pricingModelID(p.provider, p.cfg.Model.Name)
	res, err := pricing.CalculateCost(id, u.InputTokens, u.OutputTokens, u.ReasoningTokens, u.CacheReadTokens, u.CacheWriteTokens)
	if err != nil {
		log.Debugf("genkit provider: cost lookup failed for %q: %v", id, err)
		return 0
	}
	return res.TotalCost
}
