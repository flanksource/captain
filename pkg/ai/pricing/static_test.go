package pricing

import (
	"errors"
	"testing"

	"github.com/flanksource/captain/pkg/claude"
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

func TestClaudeStaticInfo(t *testing.T) {
	sonnet := claude.PricingTable[claude.ModelFamilySonnet4]
	got, ok := claudeStaticInfo("claude-sonnet-4-6")
	if !ok {
		t.Fatal("expected sonnet id to classify")
	}
	if got.InputPrice != sonnet.InputPerMTok || got.OutputPrice != sonnet.OutputPerMTok ||
		got.CacheReadsPrice != sonnet.CacheReadPerMTok || got.CacheWritesPrice != sonnet.CacheWritePerMTok {
		t.Fatalf("static sonnet price mismatch: %+v", got)
	}
	if _, ok := claudeStaticInfo("gpt-4o"); ok {
		t.Fatal("non-claude id must not classify")
	}
}

func TestApplyStaticClaudeFillsMissingKeepsOpenRouter(t *testing.T) {
	// OpenRouter gave a non-zero input price but no output price for this claude
	// row; a non-claude row must be left entirely untouched.
	withIsolatedRegistry(t, map[string]ModelInfo{
		"anthropic/claude-sonnet-4": {ModelID: "anthropic/claude-sonnet-4", InputPrice: 2.5},
		"openai/gpt-4o":             {ModelID: "openai/gpt-4o", InputPrice: 5},
	})

	applyStaticClaude()

	claudeRow, _ := GetModelInfo("anthropic/claude-sonnet-4")
	if claudeRow.InputPrice != 2.5 {
		t.Fatalf("OpenRouter input price overwritten: %+v", claudeRow)
	}
	if claudeRow.OutputPrice != claude.PricingTable[claude.ModelFamilySonnet4].OutputPerMTok {
		t.Fatalf("static output price not filled: %+v", claudeRow)
	}

	openaiRow, _ := GetModelInfo("openai/gpt-4o")
	if openaiRow.InputPrice != 5 || openaiRow.OutputPrice != 0 {
		t.Fatalf("non-claude row mutated: %+v", openaiRow)
	}
}

func TestGetModelInfoClassifyOnMiss(t *testing.T) {
	// Registry intentionally empty: a Claude id absent from OpenRouter must
	// still resolve via the static table.
	withIsolatedRegistry(t, nil)

	haiku := claude.PricingTable[claude.ModelFamilyHaiku4]
	got, ok := GetModelInfo("claude-haiku-4-5")
	if !ok {
		t.Fatal("expected classify-on-miss to price a known claude family")
	}
	if got.InputPrice != haiku.InputPerMTok || got.OutputPrice != haiku.OutputPerMTok {
		t.Fatalf("classify-on-miss price mismatch: %+v", got)
	}

	if _, ok := GetModelInfo("totally-unknown-model-zzz"); ok {
		t.Fatal("unknown non-claude id must remain a miss")
	}
}
