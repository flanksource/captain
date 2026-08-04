package ai

import (
	"testing"

	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/captain/pkg/api/registry"
)

// knownPricingGaps are preferred catalog models the pricing source does not list
// under any key, so captain reports $0 for them. These are upstream data gaps,
// not prefix bugs: the id resolves to no entry whether or not it is namespaced.
// Listed explicitly so the hole is visible and shrinks when upstream catches up,
// rather than being hidden by a permissive assertion.
//
// Empty since the generated catalog carries its own list prices: a model captain
// knows about now prices even when OpenRouter never lists it — the api-only
// "gpt-5.6" base id (OpenRouter has only the {sol,terra,luna} variants) was the
// last hole. Keep the mechanism for the next upstream gap.
var knownPricingGaps = map[string]string{}

// TestPricingIDsCoverEveryCatalogModel guards the failure mode that motivated
// unifying the pricing prefixes: a wrong namespace does not error, it just
// misses, and the run reports $0.
//
// Gemini is the trap — its catalog namespace is "googleai" but OpenRouter keys
// it under "google" — and captain had three hand-written copies of that mapping
// (PricingIDs, orPrefix, pricingModelID) that could drift apart independently.
func TestPricingIDsCoverEveryCatalogModel(t *testing.T) {
	pricing.EnsureLoaded(pricing.LoadOptions{})

	for _, p := range registry.Providers() {
		backend, err := p.BackendFor(registry.ModeAPI)
		if err != nil {
			t.Fatalf("%s has no API backend: %v", p.Name, err)
		}
		for _, m := range p.Models() {
			if !m.Preferred {
				continue
			}
			t.Run(p.Name+"/"+m.ID, func(t *testing.T) {
				info, ok := lookupPricing(backend, m.ID)
				if reason, gap := knownPricingGaps[m.ID]; gap {
					if ok {
						t.Fatalf("%s now has pricing — remove it from knownPricingGaps (%s)", m.ID, reason)
					}
					t.Skipf("known upstream pricing gap: %s", reason)
				}
				if !ok {
					t.Fatalf("no pricing for %s via %v; a missing prefix reports $0 rather than failing",
						m.ID, PricingIDs(backend, m.ID))
				}
				if info.InputPrice <= 0 && info.OutputPrice <= 0 {
					t.Errorf("%s priced at zero (input=%v output=%v)", m.ID, info.InputPrice, info.OutputPrice)
				}
			})
		}
	}
}

// TestPricingPrefixIsNotCatalogPrefix pins the two namespaces apart. If someone
// "simplifies" PricingPrefix to reuse CatalogPrefix, every Gemini price silently
// resolves to nothing — this fails first.
func TestPricingPrefixIsNotCatalogPrefix(t *testing.T) {
	if registry.Google.CatalogPrefix != "googleai" {
		t.Errorf("Google.CatalogPrefix = %q, want googleai (genkit/menu namespace)", registry.Google.CatalogPrefix)
	}
	if registry.Google.PricingPrefix != "google" {
		t.Errorf("Google.PricingPrefix = %q, want google (OpenRouter key)", registry.Google.PricingPrefix)
	}
}

// TestPricingAgreesAcrossCatalogAndBilling: the catalog and the cost path must
// price a model identically. They used to run different lookups — the catalog
// tried the bare id first (which the classify-on-miss Claude family table always
// answered, at the wrong rate) while billing tried the prefixed key first — so
// the price shown could differ from the price charged.
func TestPricingAgreesAcrossCatalogAndBilling(t *testing.T) {
	pricing.EnsureLoaded(pricing.LoadOptions{})

	for _, model := range []string{"claude-sonnet-5", "claude-opus-4-8"} {
		t.Run(model, func(t *testing.T) {
			catalog, ok := lookupPricing(BackendAnthropic, model)
			if !ok {
				t.Fatalf("catalog has no price for %s", model)
			}
			var billing pricing.ModelInfo
			for _, id := range PricingIDs(BackendAnthropic, model) {
				if info, found := pricing.GetModelInfo(id); found {
					billing = info
					break
				}
			}
			if catalog.InputPrice != billing.InputPrice || catalog.OutputPrice != billing.OutputPrice {
				t.Errorf("catalog prices %s at in=%v/out=%v but billing uses in=%v/out=%v",
					model, catalog.InputPrice, catalog.OutputPrice, billing.InputPrice, billing.OutputPrice)
			}
		})
	}
}
