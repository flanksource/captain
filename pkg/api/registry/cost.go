package registry

import (
	"regexp"
	"strings"
)

// ModelCost is a model's published list price in USD per million tokens, as
// carried in the generated catalog. It is the single source of truth for what a
// model charges. Captain previously kept a hand-written Claude family table
// beside the catalog, which classified on the substring "opus" alone and so
// billed every Opus at the 4.1 rate ($15/$75) long after 4.5 cut it to $5/$25.
type ModelCost struct {
	Input      float64 `json:"input,omitempty"`
	Output     float64 `json:"output,omitempty"`
	CacheRead  float64 `json:"cacheRead,omitempty"`
	CacheWrite float64 `json:"cacheWrite,omitempty"`
}

// snapshotSuffix matches the trailing -YYYYMMDD providers append to a pinned
// model snapshot ("claude-opus-4-5-20251101").
var snapshotSuffix = regexp.MustCompile(`-\d{8}$`)

// CostFor resolves a model's list price, accepting aliases, provider
// namespaces, dated snapshot ids, and version lines. ok is false when the
// catalog snapshot carries no price for the model; callers must report that as
// unknown rather than as zero, and must never substitute a related model's
// price — pricing one version off another is the defect this replaced.
func CostFor(model string) (ModelCost, bool) {
	p, token, _, ok := ProviderForToken(model)
	if !ok {
		return ModelCost{}, false
	}
	token = resolveAlias(strings.TrimSpace(token))
	if c, ok := costOf(p.lookupExact(token)); ok {
		return c, true
	}
	// An unpinned snapshot prices as the line it snapshots: providers publish one
	// price per model version, not per dated build.
	if bare := snapshotSuffix.ReplaceAllString(token, ""); bare != token {
		if c, ok := costOf(p.lookupExact(bare)); ok {
			return c, true
		}
	}
	// Fall back to matching on the parsed version, which catches spellings the
	// catalog does not hold verbatim — notably OpenRouter's dotted
	// "claude-opus-4.5" against the registry's dashed "claude-opus-4-5". The
	// version must match exactly: the prefix matching used for routing would
	// resolve "claude-opus-4" onto 4.8 and bill a retired model at a rate it
	// never charged.
	identity, ok := p.ParseIdentity(token)
	if !ok || identity.Version == "" {
		return ModelCost{}, false
	}
	version := normalizeModelVersion(identity.Version)
	for _, m := range knownModels {
		if m.Provider != identity.Provider || m.Family != identity.Family {
			continue
		}
		if normalizeModelVersion(m.Version) != version {
			continue
		}
		if c, ok := costOf(m, true); ok {
			return c, true
		}
	}
	return ModelCost{}, false
}

func costOf(m KnownModel, found bool) (ModelCost, bool) {
	if !found || m.Cost == nil {
		return ModelCost{}, false
	}
	return *m.Cost, true
}
