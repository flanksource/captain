package ai

import "github.com/flanksource/captain/pkg/ai/pricing"

func init() {
	RegisterDefaultModels()
}

// RegisterDefaultModels populates the pricing registry with built-in model data.
// This ensures pricing/context info is available even without the OpenRouter API.
func RegisterDefaultModels() {
	models := make(map[string]*pricing.ModelInfo)

	for _, m := range defaultModels {
		models[m.ID] = &pricing.ModelInfo{
			ModelID:          m.ID,
			ContextWindow:    m.ContextWindow,
			MaxTokens:        m.MaxTokens,
			InputPrice:       m.InputPrice,
			OutputPrice:      m.OutputPrice,
			CacheReadsPrice:  m.CacheRead,
			CacheWritesPrice: m.CacheWrite,
		}
	}

	pricing.MergeModels(models)
}
