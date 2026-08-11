// Package genkit implements captain's API-backed providers (Anthropic, OpenAI,
// Gemini) on top of Firebase Genkit, replacing the per-SDK providers. One
// Provider type serves all three backends; the plugin and model ref are chosen
// from ai.Config.Backend/Model.
//
// The exported signatures (New, the four interface methods) are FIXED —
// pkg/ai/provider/init.go registers genkit.New for the API backends against
// these signatures.
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
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/commons/logger"

	gkai "github.com/firebase/genkit/go/ai"
	gk "github.com/firebase/genkit/go/genkit"
)

// log is the package-scoped logger for AI providers. Its level follows
// -v/--log-level and can be tuned with -Plog.level.ai=debug.
var log = logger.GetLogger("ai")

// Provider is a genkit-backed ai.StreamingProvider for one API backend.
type Provider struct {
	cfg             ai.Config
	backend         ai.Backend
	g               *gk.Genkit
	modelRef        string
	toolOptionsMu   sync.Mutex
	toolCorrelation *toolEventCorrelation
}

var _ ai.StreamingProvider = (*Provider)(nil)

// New builds a genkit provider for cfg.Backend (anthropic/openai/gemini),
// inferring the backend from the model when unset. It fails loud on an
// unsupported backend or a missing API key.
func New(cfg ai.Config) (*Provider, error) {
	backend := cfg.Model.Backend
	if backend == "" {
		inferred, err := ai.InferBackend(cfg.Model.Name)
		if err != nil {
			return nil, err
		}
		backend = inferred
	}

	switch backend {
	case ai.BackendAnthropic, ai.BackendOpenAI, ai.BackendGemini, ai.BackendDeepSeek:
	default:
		return nil, fmt.Errorf("genkit provider does not support backend %q (supported: anthropic, openai, gemini, deepseek)", backend)
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
		return nil, fmt.Errorf("%w: genkit provider has no API key for backend %q (set the provider's API key, e.g. ANTHROPIC_API_KEY/OPENAI_API_KEY/GEMINI_API_KEY/DEEPSEEK_API_KEY)", ai.ErrNoAPIKey, backend)
	}

	cfg.Model.Backend = backend
	cfg.Model.Name = ai.NormalizeModelForBackend(backend, cfg.Model.Name)

	ref, err := modelRef(backend, cfg.Model.Name)
	if err != nil {
		return nil, err
	}

	g, err := getInstance(context.Background(), backend, apiKey, cfg.APIURL)
	if err != nil {
		return nil, err
	}

	return &Provider{cfg: cfg, backend: backend, g: g, modelRef: ref}, nil
}

func (p *Provider) GetModel() string       { return p.cfg.Model.Name }
func (p *Provider) GetBackend() ai.Backend { return p.backend }

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
		return nil, fmt.Errorf("genkit %s generate: %w", p.backend, err)
	}

	out := responseToResponse(resp, p.backend, p.cfg.Model.Name, start)
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
		log.Debugf("genkit %s cost: $%.6f (model=%s)", p.backend, cost, p.cfg.Model.Name)
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
		usage := mapUsage(resp.Usage, p.backend)
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
			Usage:        &usage,
			CostUSD:      p.costUSD(usage),
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
	// is a separate policy this backend has no seam for, so it must not be
	// accepted and ignored.
	if err := api.RequireToolPolicySupport(p.backend, req.Permissions); err != nil {
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

// pricingModelID maps a backend+model onto the OpenRouter-style id the pricing
// registry is keyed on (note: Gemini is google/<model>, not googleai/<model>).
// pricingModelID is the OpenRouter pricing key for a backend+model. It uses the
// provider's PricingPrefix — "google" for Gemini, whose catalog namespace is
// "googleai" (see modelRef). The two must not be derived from one another.
func pricingModelID(backend ai.Backend, model string) string {
	p, _, ok := registry.ProviderFor(backend)
	if !ok {
		return model
	}
	return p.PricingIDs(model)[0]
}

// costUSD prices a generation. Genkit usage carries no cost, so it is computed
// from the pricing registry — best effort: an unpriced model logs at debug and
// returns 0 rather than failing the request.
func (p *Provider) costUSD(u ai.Usage) float64 {
	id := pricingModelID(p.backend, p.cfg.Model.Name)
	res, err := pricing.CalculateCost(id, u.InputTokens, u.OutputTokens, u.ReasoningTokens, u.CacheReadTokens, u.CacheWriteTokens)
	if err != nil {
		log.Debugf("genkit provider: cost lookup failed for %q: %v", id, err)
		return 0
	}
	return res.TotalCost
}
