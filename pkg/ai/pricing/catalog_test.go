package pricing

import (
	"errors"
	"testing"

	catalog "github.com/flanksource/captain/pkg/api/registry"
)

// errTestShortCircuit makes EnsureLoaded return immediately so lookups in these
// tests never touch the network or the on-disk OpenRouter cache.
var errTestShortCircuit = errors.New("test: skip EnsureLoaded")

// withIsolatedRegistry swaps the package-global registry (and short-circuits
// EnsureLoaded) for the duration of a test, restoring both on cleanup.
func withIsolatedRegistry(t *testing.T, seed map[string]ModelInfo) {
	t.Helper()

	registryMu.Lock()
	savedReg := registry
	registry = map[string]ModelInfo{}
	for k, v := range seed {
		registry[k] = v
	}
	registryMu.Unlock()

	pricingCacheLock.Lock()
	savedErr := pricingCacheErr
	savedCache := pricingCache
	pricingCacheErr = errTestShortCircuit
	pricingCacheLock.Unlock()

	t.Cleanup(func() {
		registryMu.Lock()
		registry = savedReg
		registryMu.Unlock()

		pricingCacheLock.Lock()
		pricingCacheErr = savedErr
		pricingCache = savedCache
		pricingCacheLock.Unlock()
	})
}

func TestMergeModelFillMissingKeepsNonZero(t *testing.T) {
	withIsolatedRegistry(t, map[string]ModelInfo{
		"x/model": {ModelID: "x/model", InputPrice: 3, ContextWindow: 200000},
	})

	// Fill-missing must keep the existing InputPrice/ContextWindow and only set
	// the zero fields (OutputPrice, CacheReadsPrice).
	MergeModel("x/model", ModelInfo{InputPrice: 99, OutputPrice: 15, CacheReadsPrice: 0.3, ContextWindow: 1}, MergeFillMissing)

	got, _ := GetModelInfo("x/model")
	if got.InputPrice != 3 || got.ContextWindow != 200000 {
		t.Fatalf("fill-missing clobbered non-zero fields: %+v", got)
	}
	if got.OutputPrice != 15 || got.CacheReadsPrice != 0.3 {
		t.Fatalf("fill-missing did not fill zero fields: %+v", got)
	}
}

func TestMergeModelOverlayReplaces(t *testing.T) {
	withIsolatedRegistry(t, map[string]ModelInfo{
		"x/model": {ModelID: "x/model", InputPrice: 3, OutputPrice: 15},
	})

	MergeModel("x/model", ModelInfo{InputPrice: 99}, MergeOverlay)

	got, _ := GetModelInfo("x/model")
	if got.InputPrice != 99 || got.OutputPrice != 0 {
		t.Fatalf("overlay did not fully replace row: %+v", got)
	}
}

func TestCatalogInfo(t *testing.T) {
	for _, id := range []string{"claude-sonnet-4-6", "gemini-3.5-flash", "gpt-5.6"} {
		want, ok := catalog.CostFor(id)
		if !ok {
			t.Fatalf("catalog must price %q", id)
		}
		got, ok := catalogInfo(id)
		if !ok {
			t.Fatalf("catalogInfo(%q) found no price", id)
		}
		if got.InputPrice != want.Input || got.OutputPrice != want.Output ||
			got.CacheReadsPrice != want.CacheRead || got.CacheWritesPrice != want.CacheWrite {
			t.Errorf("catalogInfo(%q) = %+v, want catalog rate %+v", id, got, want)
		}
	}
	if _, ok := catalogInfo("totally-unknown-model-zzz"); ok {
		t.Fatal("an id the catalog does not price must not synthesize one")
	}
}

func TestApplyCatalogPricesFillsMissingKeepsOpenRouter(t *testing.T) {
	// OpenRouter gave a non-zero input price but no output price for this row;
	// a row the catalog does not price must be left entirely untouched.
	withIsolatedRegistry(t, map[string]ModelInfo{
		"anthropic/claude-sonnet-4-6": {ModelID: "anthropic/claude-sonnet-4-6", InputPrice: 2.5},
		"openai/gpt-4o":               {ModelID: "openai/gpt-4o", InputPrice: 5},
	})

	applyCatalogPrices()

	sonnet, _ := catalog.CostFor("claude-sonnet-4-6")
	claudeRow, _ := GetModelInfo("anthropic/claude-sonnet-4-6")
	if claudeRow.InputPrice != 2.5 {
		t.Fatalf("OpenRouter input price overwritten: %+v", claudeRow)
	}
	if claudeRow.OutputPrice != sonnet.Output {
		t.Fatalf("catalog output price not filled: %+v", claudeRow)
	}

	openaiRow, _ := GetModelInfo("openai/gpt-4o")
	if openaiRow.InputPrice != 5 || openaiRow.OutputPrice != 0 {
		t.Fatalf("unpriced row mutated: %+v", openaiRow)
	}
}

// TestGetModelInfoLookupOnMiss pins that catalog models OpenRouter never lists
// still price — and that the fallback is per-model, not per-family: Opus must
// not come back at the retired $15/$75 that the old static table applied to
// every id containing "opus".
func TestGetModelInfoLookupOnMiss(t *testing.T) {
	withIsolatedRegistry(t, nil)

	for _, id := range []string{"claude-haiku-4-5", "claude-opus-5", "gemini-3.5-flash"} {
		want, _ := catalog.CostFor(id)
		got, ok := GetModelInfo(id)
		if !ok {
			t.Fatalf("lookup-on-miss did not price catalog model %q", id)
		}
		if got.InputPrice != want.Input || got.OutputPrice != want.Output {
			t.Errorf("GetModelInfo(%q) = %.2f/%.2f, want %.2f/%.2f",
				id, got.InputPrice, got.OutputPrice, want.Input, want.Output)
		}
	}

	if opus, _ := GetModelInfo("claude-opus-5"); opus.InputPrice == 15 || opus.OutputPrice == 75 {
		t.Errorf("claude-opus-5 priced at the retired Opus 4.1 rate: %+v", opus)
	}

	if _, ok := GetModelInfo("totally-unknown-model-zzz"); ok {
		t.Fatal("unknown id must remain a miss")
	}
}
