package ai

import (
	"errors"
	"testing"
	"time"
)

func TestCachedAdaptersReusesWithinTTL(t *testing.T) {
	prevProbe := adapterProbe
	prevCache, prevAt := adapterCache, adapterCacheAt
	t.Cleanup(func() {
		adapterProbe = prevProbe
		adapterCache, adapterCacheAt = prevCache, prevAt
	})

	stub := []AdapterStatus{{Backend: string(BackendAnthropic), Type: "api"}}
	calls := 0
	adapterProbe = func() ([]AdapterStatus, error) {
		calls++
		return stub, nil
	}
	adapterCache, adapterCacheAt = nil, time.Time{}

	base := time.Unix(1_000_000, 0)
	if _, err := CachedAdapters(base); err != nil {
		t.Fatalf("CachedAdapters: %v", err)
	}
	if _, err := CachedAdapters(base.Add(10 * time.Second)); err != nil {
		t.Fatalf("CachedAdapters: %v", err)
	}
	if calls != 1 {
		t.Errorf("probe called %d times within TTL, want 1", calls)
	}
	if _, err := CachedAdapters(base.Add(2 * adapterCacheTTL)); err != nil {
		t.Fatalf("CachedAdapters: %v", err)
	}
	if calls != 2 {
		t.Errorf("probe called %d times after TTL expiry, want 2", calls)
	}
}

func TestCachedAdaptersDoesNotCacheErrors(t *testing.T) {
	prevProbe := adapterProbe
	prevCache, prevAt := adapterCache, adapterCacheAt
	t.Cleanup(func() {
		adapterProbe = prevProbe
		adapterCache, adapterCacheAt = prevCache, prevAt
	})
	adapterCache, adapterCacheAt = nil, time.Time{}

	calls := 0
	adapterProbe = func() ([]AdapterStatus, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("transient probe failure")
		}
		return []AdapterStatus{{Backend: string(BackendOpenAI), Type: "api"}}, nil
	}

	base := time.Unix(2_000_000, 0)
	if _, err := CachedAdapters(base); err == nil {
		t.Fatal("expected the transient probe error to surface")
	}
	// The next call within the TTL window must retry rather than serve a cached
	// (empty) failure.
	got, err := CachedAdapters(base.Add(time.Second))
	if err != nil {
		t.Fatalf("CachedAdapters retry: %v", err)
	}
	if len(got) != 1 || got[0].Backend != string(BackendOpenAI) {
		t.Fatalf("retry adapters = %+v, want the successful probe result", got)
	}
	if calls != 2 {
		t.Errorf("probe called %d times, want 2 (error not cached)", calls)
	}
}
