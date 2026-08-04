package pricing

import (
	"errors"
	"testing"
	"time"
)

// withStubbedFetch replaces the OpenRouter fetch and installs a fresh in-memory
// snapshot, so EnsureLoaded can be exercised without network or disk access.
func withStubbedFetch(t *testing.T, cached *PricingCache, models map[string]*ModelInfo, fetchErr error) *int {
	t.Helper()

	calls := 0
	prevFetch := fetchPricing
	fetchPricing = func() (map[string]*ModelInfo, error) {
		calls++
		return models, fetchErr
	}

	pricingCacheLock.Lock()
	savedCache, savedErr := pricingCache, pricingCacheErr
	pricingCache, pricingCacheErr = cached, nil
	pricingCacheLock.Unlock()

	t.Cleanup(func() {
		fetchPricing = prevFetch
		pricingCacheLock.Lock()
		pricingCache, pricingCacheErr = savedCache, savedErr
		pricingCacheLock.Unlock()
	})
	return &calls
}

func freshSnapshot() *PricingCache {
	return &PricingCache{
		Timestamp: time.Now().Add(-time.Hour),
		Models:    map[string]*ModelInfo{"x/cached": {ModelID: "x/cached", InputPrice: 1}},
	}
}

func TestEnsureLoadedReusesFreshSnapshot(t *testing.T) {
	calls := withStubbedFetch(t, freshSnapshot(), nil, nil)

	EnsureLoaded(LoadOptions{})

	if *calls != 0 {
		t.Fatalf("fetch calls = %d, want 0: a fresh snapshot must not re-query OpenRouter", *calls)
	}
}

func TestEnsureLoadedRefreshRefetchesFreshSnapshot(t *testing.T) {
	refreshed := map[string]*ModelInfo{"x/refreshed": {ModelID: "x/refreshed", InputPrice: 2}}
	calls := withStubbedFetch(t, freshSnapshot(), refreshed, nil)

	EnsureLoaded(LoadOptions{Refresh: true})

	if *calls != 1 {
		t.Fatalf("fetch calls = %d, want 1: refresh must bypass the unexpired snapshot", *calls)
	}
	if info, ok := GetModelInfo("x/refreshed"); !ok || info.InputPrice != 2 {
		t.Fatalf("GetModelInfo(x/refreshed) = %+v, %v; want the refreshed price installed", info, ok)
	}
}

func TestEnsureLoadedRefreshKeepsSnapshotWhenFetchFails(t *testing.T) {
	cached := freshSnapshot()
	calls := withStubbedFetch(t, cached, nil, errors.New("openrouter unreachable"))

	EnsureLoaded(LoadOptions{Refresh: true})

	if *calls != 1 {
		t.Fatalf("fetch calls = %d, want 1", *calls)
	}
	pricingCacheLock.Lock()
	defer pricingCacheLock.Unlock()
	if pricingCache != cached {
		t.Fatalf("pricingCache = %+v, want the previous snapshot retained after a failed refresh", pricingCache)
	}
}
