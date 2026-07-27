package pricing

// catalog is the generated model catalog. It is aliased because this package's
// own `registry` identifier is the OpenRouter-backed price map below.
import catalog "github.com/flanksource/captain/pkg/api/registry"

// catalogInfo synthesizes a ModelInfo from the generated model catalog, which
// carries models.dev's published rate for every model captain can route to. It
// returns false for ids the catalog does not price. ContextWindow/MaxTokens are
// left zero — this fills prices only; context is supplied separately (catalog
// metadata or OpenRouter).
//
// This replaced a hand-written Claude family table that priced on the substrings
// "opus"/"sonnet"/"haiku": it billed every Opus at the retired 4.1 rate and had
// no entry at all for Fable, which therefore fell through to Sonnet's rate.
func catalogInfo(id string) (ModelInfo, bool) {
	cost, ok := catalog.CostFor(id)
	if !ok {
		return ModelInfo{}, false
	}
	return ModelInfo{
		ModelID:          id,
		InputPrice:       cost.Input,
		OutputPrice:      cost.Output,
		CacheReadsPrice:  cost.CacheRead,
		CacheWritesPrice: cost.CacheWrite,
	}, true
}

// applyCatalogPrices overlays the catalog's rates onto registry rows, filling
// only the price fields OpenRouter left zero (OpenRouter stays primary, per the
// cost-merge decision). It does not add new ids: an id absent from the registry
// is priced lazily via catalogInfo on lookup (see GetModelInfo's lookup-on-miss).
func applyCatalogPrices() {
	registryMu.RLock()
	ids := make([]string, 0, len(registry))
	for id := range registry {
		ids = append(ids, id)
	}
	registryMu.RUnlock()

	for _, id := range ids {
		if info, ok := catalogInfo(id); ok {
			MergeModel(id, info, MergeFillMissing)
		}
	}
}
