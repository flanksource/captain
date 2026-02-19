package middleware

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/captain/pkg/ai/session"
	"github.com/flanksource/commons/logger"
)

type costProvider struct {
	provider  ai.Provider
	session   *session.Session
	budgetUSD float64
}

func (c *costProvider) GetModel() string      { return c.provider.GetModel() }
func (c *costProvider) GetBackend() ai.Backend { return c.provider.GetBackend() }

func (c *costProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if c.budgetUSD > 0 && c.session.TotalCost() >= c.budgetUSD {
		return nil, fmt.Errorf("%w: spent $%.4f of $%.4f budget",
			ai.ErrBudgetExceeded, c.session.TotalCost(), c.budgetUSD)
	}

	resp, err := c.provider.Execute(ctx, req)
	if err != nil {
		return resp, err
	}

	model := "anthropic/" + resp.Model
	if resp.Backend == ai.BackendGemini || resp.Backend == ai.BackendGeminiCLI {
		model = "google/" + resp.Model
	}

	result, calcErr := pricing.CalculateCost(model,
		resp.Usage.InputTokens, resp.Usage.OutputTokens,
		resp.Usage.ReasoningTokens, resp.Usage.CacheReadTokens, resp.Usage.CacheWriteTokens)

	if calcErr != nil {
		logger.Debugf("Cost calculation failed for %s: %v", resp.Model, calcErr)
	} else {
		c.session.AddCost(ai.Cost{
			Model:        resp.Model,
			InputTokens:  result.InputTokens,
			OutputTokens: result.OutputTokens,
			TotalTokens:  resp.Usage.TotalTokens(),
			InputCost:    result.TotalCost * float64(result.InputTokens) / float64(max(result.InputTokens+result.OutputTokens, 1)),
			OutputCost:   result.TotalCost * float64(result.OutputTokens) / float64(max(result.InputTokens+result.OutputTokens, 1)),
		})
	}

	return resp, nil
}

func WithCostTracking(sess *session.Session, budgetUSD float64) Option {
	return func(p ai.Provider) (ai.Provider, error) {
		return &costProvider{provider: p, session: sess, budgetUSD: budgetUSD}, nil
	}
}
