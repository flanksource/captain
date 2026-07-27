package claude

import (
	"sort"

	"github.com/segmentio/encoding/json"

	"github.com/flanksource/captain/pkg/api/registry"
)

// ModelPricing is a model's list price in USD per million tokens.
type ModelPricing struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheWritePerMTok float64
	CacheReadPerMTok  float64
}

// PricingFor reads a model's list price from the generated catalog, which
// carries models.dev's per-model rates. ok is false when the catalog snapshot
// prices no such model; callers must render that as "unknown", never as another
// model's rate. This replaced a hand-written opus/sonnet/haiku family table that
// classified on substring alone and so kept billing Opus 4.5 and newer at the
// retired 4.1 rate of $15/$75 rather than $5/$25.
func PricingFor(model string) (ModelPricing, bool) {
	cost, ok := registry.CostFor(model)
	if !ok {
		return ModelPricing{}, false
	}
	return ModelPricing{
		InputPerMTok:      cost.Input,
		OutputPerMTok:     cost.Output,
		CacheWritePerMTok: cost.CacheWrite,
		CacheReadPerMTok:  cost.CacheRead,
	}, true
}

// CalculateCost totals a usage record in USD, returning 0 for a model the
// catalog does not price.
func CalculateCost(usage *Usage, model string) float64 {
	if usage == nil {
		return 0
	}
	pricing, ok := PricingFor(model)
	if !ok {
		return 0
	}
	return float64(usage.InputTokens)*pricing.InputPerMTok/1e6 +
		float64(usage.OutputTokens)*pricing.OutputPerMTok/1e6 +
		float64(usage.CacheCreationInputTokens)*pricing.CacheWritePerMTok/1e6 +
		float64(usage.CacheReadInputTokens)*pricing.CacheReadPerMTok/1e6
}

// TokenSummary aggregates token counts and cost across multiple messages.
type TokenSummary struct {
	InputTokens      int     `json:"inputTokens" pretty:"label=Input"`
	OutputTokens     int     `json:"outputTokens" pretty:"label=Output"`
	CacheWriteTokens int     `json:"cacheWriteTokens" pretty:"label=Cache Write"`
	CacheReadTokens  int     `json:"cacheReadTokens" pretty:"label=Cache Read"`
	TotalCost        float64 `json:"totalCost"`
}

func (s *TokenSummary) Add(usage *Usage, model string) {
	if usage == nil {
		return
	}
	s.InputTokens += usage.InputTokens
	s.OutputTokens += usage.OutputTokens
	s.CacheWriteTokens += usage.CacheCreationInputTokens
	s.CacheReadTokens += usage.CacheReadInputTokens
	s.TotalCost += CalculateCost(usage, model)
}

func (s *TokenSummary) TotalTokens() int {
	return s.InputTokens + s.OutputTokens + s.CacheWriteTokens + s.CacheReadTokens
}

// EstimateTokens estimates token count from text using ~4 characters per token heuristic.
func EstimateTokens(text string) int {
	if len(text) == 0 {
		return 0
	}
	return (len(text) + 3) / 4
}

// EstimateContentTokens estimates tokens from a JSON raw message.
func EstimateContentTokens(content json.RawMessage) int {
	if len(content) == 0 {
		return 0
	}
	return EstimateTokens(string(content))
}

// ToolTokenSummary aggregates token estimates for a single tool type.
type ToolTokenSummary struct {
	Tool         string `json:"tool" pretty:"label=Tool,table"`
	CallCount    int    `json:"callCount" pretty:"label=Calls,table"`
	InputTokens  int    `json:"inputTokens" pretty:"label=Input,table"`
	OutputTokens int    `json:"outputTokens" pretty:"label=Output,table"`
	ErrorCount   int    `json:"errorCount" pretty:"label=Errors,table"`
}

func (t ToolTokenSummary) TotalTokens() int {
	return t.InputTokens + t.OutputTokens
}

// AggregateByTool groups tool uses by tool name and sums their estimated tokens.
func AggregateByTool(toolUses []ToolUse) []ToolTokenSummary {
	byTool := make(map[string]*ToolTokenSummary)
	for _, tu := range toolUses {
		name := tu.DisplayTool()
		s, ok := byTool[name]
		if !ok {
			s = &ToolTokenSummary{Tool: name}
			byTool[name] = s
		}
		s.CallCount++
		s.InputTokens += tu.InputTokens
		s.OutputTokens += tu.OutputTokens
		if tu.IsError {
			s.ErrorCount++
		}
	}

	result := make([]ToolTokenSummary, 0, len(byTool))
	for _, s := range byTool {
		result = append(result, *s)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].TotalTokens() > result[j].TotalTokens()
	})
	return result
}
