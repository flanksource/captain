// Package genkit implements Captain's Genkit-backed API providers. The runtime
// registry uses it for Anthropic, Gemini, and DeepSeek; OpenAI compatibility is
// retained for direct consumers while Captain's OpenAI runtime uses its official
// SDK and Responses API adapter.
package genkit

import (
	"context"
	"encoding/json"
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

	return &Provider{cfg: cfg, provider: provider, g: g, modelRef: ref}, nil
}

func (p *Provider) GetModel() string { return p.cfg.Model.Name }

// GetRuntime reports the (provider, mode) pair this adapter serves. genkit is
// the API mode for every family it supports.
func (p *Provider) GetRuntime() ai.Runtime { return ai.RuntimeOf(p.provider, ai.ModeAPI) }

// Execute runs one buffered (non-streaming) generation.
func (p *Provider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()

	opts, err := p.correlatedGenerateOptions(req, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	resp, err := gk.Generate(ctx, p.g, opts...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %v", ai.ErrTimeout, ctx.Err())
		}
		// genkit validates the model's constrained output against the request
		// schema during generation; a rejection is recoverable by re-asking the
		// model with the errors, so classify it as ErrSchemaValidation (preserving
		// genkit's detail lines) for the schema-validation middleware to act on.
		if isSchemaMismatch(err) {
			return nil, fmt.Errorf("%w: %v", ai.ErrSchemaValidation, err)
		}
		return nil, fmt.Errorf("genkit %s generate: %w", p.provider.Name, err)
	}

	out := responseToResponse(ctx, resp, p.backend, p.cfg.Model.Name, start)
	if resp.FinishReason == gkai.FinishReasonInterrupted {
		out.ToolApproval, err = toolApprovalState(req, resp)
		if err != nil {
			return nil, err
		}
	}

	if out.ToolApproval != nil {
		return out, nil
	}
	if req.Prompt.Schema != nil {
		if err := resp.Output(req.Prompt.Schema); err != nil {
			return nil, fmt.Errorf("%w: %v", ai.ErrSchemaValidation, err)
		}
		out.StructuredData = req.Prompt.Schema
		out.Text = ""
	} else if len(req.Prompt.SchemaJSON) > 0 {
		// A pre-built JSON schema has no Go target to bind into; genkit returns the
		// constrained JSON as text — surface it as raw structured data too.
		if out.Text != "" {
			out.StructuredData = json.RawMessage(out.Text)
		}
	}

	if cost := p.costUSD(out.Usage); cost > 0 {
		out.CostUSD = cost
		log.Debugf("genkit %s cost: $%.6f (model=%s)", p.provider.Name, cost, p.cfg.Model.Name)
	}

	return out, nil
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
// and a terminal EventResult carrying usage + best-effort cost. Structured
// output is unsupported in stream mode (mirrors the claude_cli stream provider).
func (p *Provider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	if req.Prompt.HasSchema() {
		return nil, fmt.Errorf("genkit stream mode does not support structured output; use Execute")
	}

	ch := make(chan ai.Event, 16)
	correlation := newToolEventCorrelation()
	cb := func(_ context.Context, chunk *gkai.ModelResponseChunk) error {
		events, err := chunkToEvents(chunk, p.cfg.Model.Name, correlation)
		if err != nil {
			return err
		}
		for _, ev := range events {
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

	opts, err := p.correlatedGenerateOptions(req, cb, emit, correlation)
	if err != nil {
		return nil, err
	}
	go func() {
		defer close(ch)
		resp, err := gk.Generate(ctx, p.g, opts...)
		if err != nil {
			ch <- ai.Event{Kind: ai.EventError, Error: err.Error(), Model: p.cfg.Model.Name}
			return
		}
		var usage *ai.Usage
		if resp.Usage != nil {
			mapped := mapUsage(resp.Usage, p.backend)
			usage = &mapped
		}
		costUSD := 0.0
		if usage != nil {
			costUSD = p.costUSD(*usage)
		}
		var approval *api.ToolApprovalState
		if resp.FinishReason == gkai.FinishReasonInterrupted {
			approval, err = toolApprovalState(req, resp)
			if err != nil {
				ch <- ai.Event{Kind: ai.EventError, Error: err.Error(), Model: p.cfg.Model.Name}
				return
			}
		}
		ch <- ai.Event{
			Kind:         ai.EventResult,
			Success:      true,
			Usage:        usage,
			CostUSD:      costUSD,
			Model:        p.cfg.Model.Name,
			ToolApproval: approval,
		}
	}()

	return ch, nil
}

func (p *Provider) correlatedGenerateOptions(
	req ai.Request,
	stream gkai.ModelStreamCallback,
	emit func(ai.Event),
	correlation *toolEventCorrelation,
) ([]gkai.GenerateOption, error) {
	// Caller tools are gated by ToolPreferences and CanUseTool; Permissions.Tools
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
	return generateOptions(p, req, stream, emit)
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
