package ai

import (
	"context"
	"encoding/json"
	"io"
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

// PromptRequest is a single named prompt. Field names/types mirror the former
// clicky/ai.PromptRequest so consumers need only change the import path.
type PromptRequest struct {
	Name             string            `json:"name"`
	Prompt           string            `json:"prompt"`
	SystemPrompt     string            `json:"system_prompt,omitempty"`
	Context          map[string]string `json:"context,omitempty"`
	StructuredOutput any               `json:"structured_output,omitempty"`
	// SchemaJSON is a pre-built JSON Schema (e.g. from a .prompt frontmatter
	// output block) forwarded verbatim to ai.Request.Prompt.SchemaJSON. Prefer it
	// over StructuredOutput when the schema is declared in the prompt file rather
	// than a Go type; the two are mutually exclusive.
	SchemaJSON json.RawMessage `json:"schema_json,omitempty"`
	// SchemaStrictness forwards api.Prompt.SchemaStrictness — the policy for a
	// response that fails schema validation (warning/error/retry). "" (default)
	// skips validation.
	SchemaStrictness api.SchemaStrictness `json:"schema_strictness,omitempty"`
	// Source identifies the prompt template (e.g. the .prompt filename) for
	// diagnostics; forwarded to ai.Request.Source and printed by the logging
	// middleware.
	Source string `json:"source,omitempty"`
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

// GetBackend returns the underlying backend.
func (a *Agent) GetBackend() Backend { return a.provider.GetBackend() }

// ExecutePrompt runs one prompt and accrues its cost onto the agent.
func (a *Agent) ExecutePrompt(ctx context.Context, req PromptRequest) (*PromptResponse, error) {
	start := time.Now()
	resp, err := a.provider.Execute(ctx, Request{Prompt: api.Prompt{
		User:             req.Prompt,
		System:           req.SystemPrompt,
		Source:           req.Source,
		Schema:           req.StructuredOutput,
		SchemaJSON:       req.SchemaJSON,
		SchemaStrictness: req.SchemaStrictness,
	}})
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

// Close releases the underlying provider's resources, if any.
func (a *Agent) Close() error {
	if c, ok := a.provider.(io.Closer); ok {
		return c.Close()
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
	cost := Cost{
		Model:            model,
		InputTokens:      resp.Usage.InputTokens,
		OutputTokens:     resp.Usage.OutputTokens,
		ReasoningTokens:  resp.Usage.ReasoningTokens,
		CacheReadTokens:  resp.Usage.CacheReadTokens,
		CacheWriteTokens: resp.Usage.CacheWriteTokens,
		TotalTokens:      resp.Usage.TotalTokens(),
	}
	// The pricing registry is keyed on OpenRouter-style ids (provider/model);
	// try the backend-prefixed id first, then the bare model.
	for _, id := range pricingIDs(a.provider.GetBackend(), model) {
		if res, err := pricing.CalculateCost(id, cost.InputTokens, cost.OutputTokens, resp.Usage.ReasoningTokens, resp.Usage.CacheReadTokens, resp.Usage.CacheWriteTokens); err == nil {
			cost.InputCost = res.InputCost
			cost.OutputCost = res.OutputCost
			cost.ReasoningCost = res.ReasoningCost
			cost.CacheReadCost = res.CacheReadCost
			cost.CacheWriteCost = res.CacheWriteCost
			break
		}
	}

	a.mu.Lock()
	a.costs = append(a.costs, cost)
	a.mu.Unlock()
	return cost
}

// pricingIDs returns the candidate pricing keys for a model, most specific first.
func pricingIDs(backend Backend, model string) []string {
	prefix := map[Backend]string{
		BackendAnthropic: "anthropic/",
		BackendOpenAI:    "openai/",
		BackendGemini:    "google/",
	}[backend]
	if prefix != "" {
		return []string{prefix + model, model}
	}
	return []string{model}
}

func (r *PromptResponse) errorValue() error {
	if r == nil || r.Error == "" {
		return nil
	}
	return &promptError{msg: r.Error}
}

type promptError struct{ msg string }

func (e *promptError) Error() string { return e.msg }
