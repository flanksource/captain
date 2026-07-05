package session

import (
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
)

// CostFromUsage converts a raw Claude Usage + model into the canonical api.Cost,
// computing per-bucket USD from the model's pricing (Sonnet fallback for unknown
// families). This is the single conversion point from the transcript token
// representation to the runtime-canonical cost type.
func CostFromUsage(u *claude.Usage, model string) api.Cost {
	if u == nil {
		return api.Cost{Model: model}
	}
	p := claude.PricingFor(model)
	return api.Cost{
		Model:            model,
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadInputTokens,
		CacheWriteTokens: u.CacheCreationInputTokens,
		TotalTokens:      u.InputTokens + u.OutputTokens + u.CacheReadInputTokens + u.CacheCreationInputTokens,
		InputCost:        float64(u.InputTokens) * p.InputPerMTok / 1e6,
		OutputCost:       float64(u.OutputTokens) * p.OutputPerMTok / 1e6,
		CacheReadCost:    float64(u.CacheReadInputTokens) * p.CacheReadPerMTok / 1e6,
		CacheWriteCost:   float64(u.CacheCreationInputTokens) * p.CacheWritePerMTok / 1e6,
	}
}

// CostFromModelUsage converts a per-tool tools.ModelUsage (which already carries
// a precomputed total cost) into the canonical api.Cost, recomputing per-bucket
// USD from pricing so the buckets are populated.
func CostFromModelUsage(m tools.ModelUsage) api.Cost {
	p := claude.PricingFor(m.Model)
	return api.Cost{
		Model:            m.Model,
		InputTokens:      m.InputTokens,
		OutputTokens:     m.OutputTokens,
		CacheReadTokens:  m.CacheReadInputTokens,
		CacheWriteTokens: m.CacheCreationInputTokens,
		TotalTokens:      m.InputTokens + m.OutputTokens + m.CacheReadInputTokens + m.CacheCreationInputTokens,
		InputCost:        float64(m.InputTokens) * p.InputPerMTok / 1e6,
		OutputCost:       float64(m.OutputTokens) * p.OutputPerMTok / 1e6,
		CacheReadCost:    float64(m.CacheReadInputTokens) * p.CacheReadPerMTok / 1e6,
		CacheWriteCost:   float64(m.CacheCreationInputTokens) * p.CacheWritePerMTok / 1e6,
	}
}

// usageFromCost projects an api.Cost's token buckets into an api.Usage.
func usageFromCost(c api.Cost) api.Usage {
	return api.Usage{
		InputTokens:      c.InputTokens,
		OutputTokens:     c.OutputTokens,
		CacheReadTokens:  c.CacheReadTokens,
		CacheWriteTokens: c.CacheWriteTokens,
	}
}
