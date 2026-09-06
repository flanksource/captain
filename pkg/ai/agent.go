package ai

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/captain/pkg/api"
)

// Agent is a convenience wrapper over a Provider that offers the named-prompt /
// batch / cost-accrual surface gavel (and the old clicky/ai.Agent consumers)
// code against, so those call sites migrate by swapping the import rather than
// rewriting logic. For streaming or plugin-driven runs use Provider /
// agent.Runner directly.
type Agent struct {
	provider Provider
	cfg      Config

	mu    sync.Mutex
	costs Costs
}

// PromptRequest carries the complete specification of one named model call.
type PromptRequest struct {
	Name string   `json:"name"`
	Spec api.Spec `json:"spec"`
}

// PromptResponse is the result of one PromptRequest.
type PromptResponse struct {
	Request         PromptRequest    `json:"request,omitempty"`
	Result          string           `json:"result"`
	StructuredData  any              `json:"structured_data,omitempty"`
	TerminalOutcome *TerminalOutcome `json:"terminal_outcome,omitempty"`
	Costs           Costs            `json:"costs,omitempty"`
	Model           string           `json:"model,omitempty"`
	Error           string           `json:"error,omitempty"`
	Duration        time.Duration    `json:"duration,omitempty"`
	CacheHit        bool             `json:"cache_hit,omitempty"`
}

// IsOK reports whether the prompt succeeded.
func (r PromptResponse) IsOK() bool { return r.Error == "" }

// NewAgent builds an Agent from cfg, constructing the underlying provider.
func NewAgent(cfg Config) (*Agent, error) {
	p, err := NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	return &Agent{provider: p, cfg: cfg}, nil
}

// NewAgentWithProvider wraps an already-built provider (e.g. one already
// decorated with caching/cost middleware).
func NewAgentWithProvider(p Provider, cfg Config) *Agent {
	return &Agent{provider: p, cfg: cfg}
}

// GetModel returns the configured model.
func (a *Agent) GetModel() string { return a.provider.GetModel() }

// GetRuntime returns the underlying (provider, mode) pair.
func (a *Agent) GetRuntime() Runtime { return a.provider.GetRuntime() }

// ExecutePrompt runs one prompt and accrues its cost onto the agent. It fails
// with ErrBudgetExceeded before executing once accumulated spend has reached the
// configured USD budget, so a batch of prompts cannot run unbounded.
func (a *Agent) ExecutePrompt(ctx context.Context, req PromptRequest) (*PromptResponse, error) {
	start := time.Now()
	if budget := a.cfg.Budget.Cost; budget > 0 {
		if spent := a.TotalCost(); spent >= budget {
			err := fmt.Errorf("%w: spent $%.4f of $%.4f budget", ErrBudgetExceeded, spent, budget)
			return &PromptResponse{Request: req, Model: a.cfg.Model.Name, Error: err.Error()}, err
		}
	}
	resp, err := a.provider.Execute(ctx, req.Spec)
	if err != nil {
		return &PromptResponse{Request: req, Model: a.cfg.Model.Name, Error: err.Error(), Duration: time.Since(start)}, err
	}

	cost := a.accrue(resp)
	return &PromptResponse{
		Request:         req,
		Result:          resp.Text,
		StructuredData:  resp.StructuredData,
		TerminalOutcome: resp.TerminalOutcome,
		Costs:           Costs{cost},
		Model:           resp.Model,
		Duration:        time.Since(start),
		CacheHit:        resp.CacheHit,
	}, nil
}

// ExecuteBatch runs the requests concurrently (bounded by cfg.MaxConcurrent,
// default 4) and returns the responses keyed by request Name. Per-request
// failures are reported in the response's Error; the returned error is non-nil
// only if every request failed.
func (a *Agent) ExecuteBatch(ctx context.Context, requests []PromptRequest) (map[string]*PromptResponse, error) {
	limit := a.cfg.MaxConcurrent
	if limit <= 0 {
		limit = 4
	}
	sem := make(chan struct{}, limit)

	var wg sync.WaitGroup
	var resultMu sync.Mutex
	results := make(map[string]*PromptResponse, len(requests))
	failures := 0

	for _, req := range requests {
		wg.Add(1)
		go func(req PromptRequest) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			resp, err := a.ExecutePrompt(ctx, req)
			resultMu.Lock()
			results[req.Name] = resp
			if err != nil {
				failures++
			}
			resultMu.Unlock()
		}(req)
	}
	wg.Wait()

	if len(requests) > 0 && failures == len(requests) {
		return results, results[requests[0].Name].errorValue()
	}
	return results, nil
}

// GetCosts returns the accumulated per-call costs.
func (a *Agent) GetCosts() Costs {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(Costs, len(a.costs))
	copy(out, a.costs)
	return out
}

// TotalCost returns the accumulated USD spend across every prompt run on this
// agent, preferring provider-reported cost (via Cost.Total).
func (a *Agent) TotalCost() float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.costs.Sum().Total()
}

// Close releases the underlying provider's resources, if any.
func (a *Agent) Close() error {
	if closer, ok := api.ProviderAs[api.CloseableProvider](a.provider); ok {
		return closer.Close()
	}
	return nil
}

// accrue computes the cost of a response (best effort via the pricing registry)
// and records it.
func (a *Agent) accrue(resp *Response) Cost {
	model := resp.Model
	if model == "" {
		model = a.cfg.Model.Name
	}
	p, _ := api.ProviderByName(a.provider.GetRuntime().Provider)
	cost := PriceResponse(p, model, resp)

	a.mu.Lock()
	a.costs = append(a.costs, cost)
	a.mu.Unlock()
	return cost
}

// PriceResponse builds the Cost for a response: the disjoint token buckets, the
// provider-reported total (which Cost.Total() prefers), and a best-effort
// list-price breakdown for display. A pricing miss leaves the bucket costs zero
// but preserves the token counts and any provider-reported total, so cost is
// never silently dropped just because a model is absent from the registry.
func PriceResponse(p *ModelProvider, model string, resp *Response) Cost {
	return PriceUsage(p, model, resp.Usage, resp.CostUSD)
}

// PriceUsage is PriceResponse for callers that hold a bare Usage plus the
// provider's reported total (stream events, persisted model calls) rather than
// a full Response.
func PriceUsage(p *ModelProvider, model string, usage Usage, providerCostUSD float64) Cost {
	cost := Cost{
		Model:            model,
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		ReasoningTokens:  usage.ReasoningTokens,
		CacheReadTokens:  usage.CacheReadTokens,
		CacheWriteTokens: usage.CacheWriteTokens,
		TotalTokens:      usage.TotalTokens(),
		ProviderCostUSD:  providerCostUSD,
	}
	// The pricing registry is keyed on OpenRouter-style ids (provider/model);
	// try the provider-prefixed id first, then the bare model.
	for _, id := range PricingIDs(p, model) {
		if res, err := pricing.CalculateCost(id, cost.InputTokens, cost.OutputTokens, cost.ReasoningTokens, cost.CacheReadTokens, cost.CacheWriteTokens); err == nil {
			cost.InputCost = res.InputCost
			cost.OutputCost = res.OutputCost
			cost.ReasoningCost = res.ReasoningCost
			cost.CacheReadCost = res.CacheReadCost
			cost.CacheWriteCost = res.CacheWriteCost
			break
		}
	}
	return cost
}

// ContextWindowFor returns a model's context window from the pricing registry,
// or 0 when unknown, so persisted model calls can record context occupancy
// server-side instead of relying on the UI's catalog for the denominator.
func ContextWindowFor(p *ModelProvider, model string) int {
	for _, id := range PricingIDs(p, model) {
		if info, ok := pricing.GetModelInfo(id); ok && info.ContextWindow > 0 {
			return info.ContextWindow
		}
	}
	return 0
}

// PricingIDs returns the candidate OpenRouter-style pricing keys for a model,
// most specific first. Pricing is a property of the provider alone — the local
// transports bill against the same vendor as the API mode — so the mode plays no
// part here; the bare model is included as a fallback.
func PricingIDs(p *ModelProvider, model string) []string {
	if p == nil {
		return []string{model}
	}
	return p.PricingIDs(model)
}

func (r *PromptResponse) errorValue() error {
	if r == nil || r.Error == "" {
		return nil
	}
	return &promptError{msg: r.Error}
}

type promptError struct{ msg string }

func (e *promptError) Error() string { return e.msg }
