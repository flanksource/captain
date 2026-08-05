package session

import (
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
)

// responseCosts accumulates per-API-response costs from transcript entries.
//
// This is the no-result fallback: stored claude transcripts carry no result
// record, so the session total has to be reconstructed from each response's
// usage. See api.ResponseSet for why that reconstruction needs deduplication,
// and prefer a reported result wherever one exists.
type responseCosts struct {
	responses api.ResponseSet
	costs     api.Costs
}

func newResponseCosts() *responseCosts {
	return &responseCosts{}
}

// firstSighting reports whether an entry carries usage for a response not yet
// counted, marking it counted.
func (r *responseCosts) firstSighting(entry claude.HistoryEntry) bool {
	if !entry.IsAssistantMessage() || entry.Message.Usage == nil {
		return false
	}
	return r.responses.First(entry.Message.ID)
}

// add records an assistant entry's usage, ignoring repeated content-block lines
// of a response already counted.
func (r *responseCosts) add(entry claude.HistoryEntry) {
	if !r.firstSighting(entry) {
		return
	}
	r.costs = append(r.costs, CostFromUsage(entry.Message.Usage, entry.Message.Model))
}

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
		ReasoningTokens:  c.ReasoningTokens,
		CacheReadTokens:  c.CacheReadTokens,
		CacheWriteTokens: c.CacheWriteTokens,
	}
}
