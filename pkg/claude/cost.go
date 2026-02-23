package claude

import "strings"

type ModelFamily string

const (
	ModelFamilyOpus4   ModelFamily = "opus-4"
	ModelFamilySonnet4 ModelFamily = "sonnet-4"
	ModelFamilyHaiku4  ModelFamily = "haiku-4"
	ModelFamilyUnknown ModelFamily = "unknown"
)

type ModelPricing struct {
	InputPerMTok      float64
	OutputPerMTok     float64
	CacheWritePerMTok float64
	CacheReadPerMTok  float64
}

// PricingTable maps model families to their per-million-token pricing in USD.
// Source: https://docs.anthropic.com/en/docs/about-claude/models
var PricingTable = map[ModelFamily]ModelPricing{
	ModelFamilyOpus4: {
		InputPerMTok:      15.0,
		OutputPerMTok:     75.0,
		CacheWritePerMTok: 18.75,
		CacheReadPerMTok:  1.50,
	},
	ModelFamilySonnet4: {
		InputPerMTok:      3.0,
		OutputPerMTok:     15.0,
		CacheWritePerMTok: 3.75,
		CacheReadPerMTok:  0.30,
	},
	ModelFamilyHaiku4: {
		InputPerMTok:      0.80,
		OutputPerMTok:     4.0,
		CacheWritePerMTok: 1.0,
		CacheReadPerMTok:  0.08,
	},
}

func ClassifyModel(model string) ModelFamily {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return ModelFamilyOpus4
	case strings.Contains(m, "sonnet"):
		return ModelFamilySonnet4
	case strings.Contains(m, "haiku"):
		return ModelFamilyHaiku4
	default:
		return ModelFamilyUnknown
	}
}

func CalculateCost(usage *Usage, model string) float64 {
	if usage == nil {
		return 0
	}
	family := ClassifyModel(model)
	pricing, ok := PricingTable[family]
	if !ok {
		pricing = PricingTable[ModelFamilySonnet4]
	}
	return float64(usage.InputTokens)*pricing.InputPerMTok/1e6 +
		float64(usage.OutputTokens)*pricing.OutputPerMTok/1e6 +
		float64(usage.CacheCreationInputTokens)*pricing.CacheWritePerMTok/1e6 +
		float64(usage.CacheReadInputTokens)*pricing.CacheReadPerMTok/1e6
}

// TokenSummary aggregates token counts and cost across multiple messages.
type TokenSummary struct {
	InputTokens       int     `json:"inputTokens" pretty:"label=Input"`
	OutputTokens      int     `json:"outputTokens" pretty:"label=Output"`
	CacheWriteTokens  int     `json:"cacheWriteTokens" pretty:"label=Cache Write"`
	CacheReadTokens   int     `json:"cacheReadTokens" pretty:"label=Cache Read"`
	TotalCost         float64 `json:"totalCost"`
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
