package pricing

import "github.com/flanksource/captain/pkg/claude"

// claudeStaticInfo synthesizes a ModelInfo from the hand-curated static Claude
// price table for any model id that classifies to a known Claude family
// (opus/sonnet/haiku). It returns false for non-Claude ids and unknown
// families. ContextWindow/MaxTokens are left zero — the static table carries
// prices only; context is supplied separately (catalog or OpenRouter).
func claudeStaticInfo(id string) (ModelInfo, bool) {
	p, ok := claude.PricingTable[claude.ClassifyModel(id)]
	if !ok {
		return ModelInfo{}, false
	}
	return ModelInfo{
		ModelID:          id,
		InputPrice:       p.InputPerMTok,
		OutputPrice:      p.OutputPerMTok,
		CacheReadsPrice:  p.CacheReadPerMTok,
		CacheWritesPrice: p.CacheWritePerMTok,
	}, true
}

// applyStaticClaude overlays the static Claude price table onto registry rows
// that classify to a known Claude family, filling only the price fields
// OpenRouter left zero (OpenRouter stays primary, per the cost-merge decision).
// It does not add new ids: an id absent from the registry is priced lazily via
// claudeStaticInfo on lookup (see GetModelInfo's classify-on-miss).
func applyStaticClaude() {
	registryMu.RLock()
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	registryMu.RUnlock()

	for _, id := range ids {
		if info, ok := claudeStaticInfo(id); ok {
			MergeModel(id, info, MergeFillMissing)
		}
	}
}
