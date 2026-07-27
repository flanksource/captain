package registry

import "testing"

// publishedRates are the vendors' list prices in USD per million tokens, taken
// from the provider pricing pages rather than from captain's own catalog, so
// this test fails if a regenerated models.json ever drifts from reality.
var publishedRates = map[string]ModelCost{
	"claude-opus-5":     {Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25},
	"claude-opus-4-8":   {Input: 5, Output: 25, CacheRead: 0.5, CacheWrite: 6.25},
	"claude-sonnet-5":   {Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5},
	"claude-sonnet-4-6": {Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75},
	"claude-fable-5":    {Input: 10, Output: 50, CacheRead: 1, CacheWrite: 12.5},
	"claude-haiku-4-5":  {Input: 1, Output: 5, CacheRead: 0.1, CacheWrite: 1.25},
}

// TestCostForMatchesPublishedRates is the regression guard for the family-table
// pricing defect: classifying on the substring "opus" alone billed every Opus at
// $15/$75 (the 4.1 rate) and priced Fable — which matched no family at all — at
// the Sonnet fallback. Each id here must carry its own version's price.
func TestCostForMatchesPublishedRates(t *testing.T) {
	for id, want := range publishedRates {
		t.Run(id, func(t *testing.T) {
			got, ok := CostFor(id)
			if !ok {
				t.Fatalf("CostFor(%q) found no price", id)
			}
			if got != want {
				t.Errorf("CostFor(%q) = %+v, want %+v", id, got, want)
			}
		})
	}
}

// TestCostForDistinguishesVersionsAndFamilies pins that pricing is per-model,
// not per-family: the two Opus generations and the four Claude families must not
// collapse onto one rate.
func TestCostForDistinguishesVersionsAndFamilies(t *testing.T) {
	opus, _ := CostFor("claude-opus-5")
	sonnetOld, _ := CostFor("claude-sonnet-4-6")
	sonnetNew, _ := CostFor("claude-sonnet-5")
	fable, _ := CostFor("claude-fable-5")

	if opus.Input == 15 || opus.Output == 75 {
		t.Errorf("claude-opus-5 priced at the retired Opus 4.1 rate: %+v", opus)
	}
	if sonnetNew == sonnetOld {
		t.Errorf("Sonnet 5 and Sonnet 4.6 share one rate %+v; versions must price apart", sonnetNew)
	}
	if fable == sonnetOld {
		t.Errorf("Fable priced at the Sonnet fallback %+v instead of its own rate", fable)
	}
}

// TestCostForAcceptsIDSpellings covers the id forms that reach pricing: catalog
// ids, provider namespaces, codename aliases, dated provider snapshots, and
// OpenRouter's dotted version spelling.
func TestCostForAcceptsIDSpellings(t *testing.T) {
	opus45, ok := CostFor("claude-opus-4-5")
	if !ok {
		t.Fatal("claude-opus-4-5 must be priced")
	}
	for _, spelling := range []string{
		"anthropic/claude-opus-4-5",
		"claude-opus-4-5-20251101",
		"anthropic/claude-opus-4.5",
	} {
		got, ok := CostFor(spelling)
		if !ok {
			t.Errorf("CostFor(%q) found no price", spelling)
			continue
		}
		if got != opus45 {
			t.Errorf("CostFor(%q) = %+v, want the claude-opus-4-5 rate %+v", spelling, got, opus45)
		}
	}

	viaAlias, ok := CostFor("sol")
	if !ok {
		t.Fatal("codename alias sol must resolve to a priced model")
	}
	if exact, _ := CostFor("gpt-5.6-sol"); viaAlias != exact {
		t.Errorf("alias sol priced %+v, want gpt-5.6-sol rate %+v", viaAlias, exact)
	}
}

// TestCostForRejectsUnpricedIDs pins that a miss stays a miss. Returning a
// nearby model's rate is what made the old table wrong; an unknown id must be
// reported as unknown so callers can show "unpriced" instead of a fabricated
// number.
func TestCostForRejectsUnpricedIDs(t *testing.T) {
	for _, id := range []string{
		"",
		"totally-unknown-model-zzz",
		"claude-opus", // family with no version: must not inherit the flagship rate
		"gpt",         // ditto on OpenAI
		"claude-opus-99-does-not-exist",
		// Retired, and priced $15/$75 when it shipped. Version matching must be
		// exact, or the routing rules would resolve it onto 4.8's $5/$25.
		"claude-opus-4",
	} {
		if got, ok := CostFor(id); ok {
			t.Errorf("CostFor(%q) = %+v, want no price", id, got)
		}
	}
}

// TestEveryCatalogModelIsPriced guards the generator: a models.json row with no
// cost block would silently price that model at zero everywhere.
func TestEveryCatalogModelIsPriced(t *testing.T) {
	for _, m := range knownModels {
		if m.Cost == nil {
			t.Errorf("catalog model %q carries no cost block", m.ID)
			continue
		}
		if m.Cost.Input <= 0 || m.Cost.Output <= 0 {
			t.Errorf("catalog model %q has non-positive price %+v", m.ID, *m.Cost)
		}
	}
}
