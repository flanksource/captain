package session

import (
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
)

// CostFromUsage converts a raw Claude Usage + model into the canonical api.Cost.
// This is the single conversion point from the transcript token representation
// to the runtime-canonical cost type.
func CostFromUsage(u *claude.Usage, model string) api.Cost {
	if u == nil {
		return api.Cost{Model: model}
	}
	return costOf(model, u.InputTokens, u.OutputTokens, u.CacheReadInputTokens, u.CacheCreationInputTokens)
}

// CostFromModelUsage converts a per-tool tools.ModelUsage (which already carries
// a precomputed total cost) into the canonical api.Cost, recomputing per-bucket
// USD from pricing so the buckets are populated.
func CostFromModelUsage(m tools.ModelUsage) api.Cost {
	return costOf(m.Model, m.InputTokens, m.OutputTokens, m.CacheReadInputTokens, m.CacheCreationInputTokens)
}

// costOf prices a model's token buckets. A model the catalog does not price
// keeps its token counts with zero USD: reporting an unpriced model at some
// other model's rate is exactly the defect the catalog lookup replaced.
func costOf(model string, input, output, cacheRead, cacheWrite int) api.Cost {
	cost := api.Cost{
		Model:            model,
		InputTokens:      input,
		OutputTokens:     output,
		CacheReadTokens:  cacheRead,
		CacheWriteTokens: cacheWrite,
		TotalTokens:      input + output + cacheRead + cacheWrite,
	}
	p, ok := claude.PricingFor(model)
	if !ok {
		return cost
	}
	cost.InputCost = float64(input) * p.InputPerMTok / 1e6
	cost.OutputCost = float64(output) * p.OutputPerMTok / 1e6
	cost.CacheReadCost = float64(cacheRead) * p.CacheReadPerMTok / 1e6
	cost.CacheWriteCost = float64(cacheWrite) * p.CacheWritePerMTok / 1e6
	return cost
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
