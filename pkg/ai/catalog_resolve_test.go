package ai

import (
	"context"
	"errors"
	"testing"
)

// stubLiveFetcher installs a fake live-model fetcher for the test and restores
// the real one on cleanup. It also isolates API-key env so only the backends
// the test opts into are fetched, and points the model cache at a temp HOME.
func stubLiveFetcher(t *testing.T, fn func(b Backend) ([]ModelDef, error)) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	prev := liveModelFetcher
	liveModelFetcher = func(_ context.Context, b Backend) ([]ModelDef, error) { return fn(b) }
	t.Cleanup(func() { liveModelFetcher = prev })
}

func hasModelID(rows []ResolvedModel, id string) (ResolvedModel, bool) {
	for _, r := range rows {
		if r.ID == id {
			return r, true
		}
	}
	return ResolvedModel{}, false
}

func TestResolveModels_NoTokensCatalogOnly(t *testing.T) {
	stubLiveFetcher(t, func(b Backend) ([]ModelDef, error) {
		t.Fatalf("live fetch must not run without tokens (backend %q)", b)
		return nil, nil
	})

	rows, err := ResolveModels(context.Background(), ResolveOptions{Backend: BackendAnthropic, UseTokens: false})
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected catalog anthropic models")
	}
	for _, r := range rows {
		if r.Backend != BackendAnthropic {
			t.Fatalf("backend filter leaked: %q", r.Backend)
		}
		if r.Live {
			t.Fatalf("no row should be Live without tokens: %q", r.ID)
		}
	}
}

func TestResolveModels_TokensUnionDedup(t *testing.T) {
	stubLiveFetcher(t, func(b Backend) ([]ModelDef, error) {
		if b != BackendAnthropic {
			return nil, nil
		}
		return []ModelDef{
			{ID: "claude-sonnet-5", Backend: BackendAnthropic}, // dedups with catalog anthropic/claude-sonnet-5
			{ID: "claude-new-xyz", Backend: BackendAnthropic},  // net-new live model
		}, nil
	})
	t.Setenv("ANTHROPIC_API_KEY", "k") // after stub clears the key set

	rows, err := ResolveModels(context.Background(), ResolveOptions{Backend: BackendAnthropic, UseTokens: true})
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}

	catalogRow, ok := hasModelID(rows, "anthropic/claude-sonnet-5")
	if !ok || !catalogRow.Live {
		t.Fatalf("catalog row should survive and be marked Live: %+v ok=%v", catalogRow, ok)
	}
	liveRow, ok := hasModelID(rows, "claude-new-xyz")
	if !ok || !liveRow.Live {
		t.Fatalf("net-new live model missing/!Live: %+v ok=%v", liveRow, ok)
	}

	// No duplicate (backend, bareID) for claude-sonnet-5.
	bareCount := 0
	for _, r := range rows {
		if r.Backend == BackendAnthropic && r.BareID() == "claude-sonnet-5" {
			bareCount++
		}
	}
	if bareCount != 1 {
		t.Fatalf("claude-sonnet-5 appears %d times, want deduped to 1", bareCount)
	}
}

func TestResolveModels_LiveErrorFailsLoud(t *testing.T) {
	stubLiveFetcher(t, func(b Backend) ([]ModelDef, error) {
		return nil, errors.New("boom")
	})
	t.Setenv("ANTHROPIC_API_KEY", "k") // after stub clears the key set

	if _, err := ResolveModels(context.Background(), ResolveOptions{Backend: BackendAnthropic, UseTokens: true}); err == nil {
		t.Fatal("expected live fetch error to propagate (fail loud)")
	}
}

func TestResolveModels_LegacyHiddenUnlessFiltered(t *testing.T) {
	stubLiveFetcher(t, func(b Backend) ([]ModelDef, error) {
		if b != BackendAnthropic {
			return nil, nil
		}
		return []ModelDef{{ID: "claude-3-5-sonnet-20241022", Backend: BackendAnthropic}}, nil
	})
	t.Setenv("ANTHROPIC_API_KEY", "k") // after stub clears the key set

	noFilter, err := ResolveModels(context.Background(), ResolveOptions{Backend: BackendAnthropic, UseTokens: true})
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	if _, ok := hasModelID(noFilter, "claude-3-5-sonnet-20241022"); ok {
		t.Fatal("legacy id should be hidden without a filter")
	}

	withFilter, err := ResolveModels(context.Background(), ResolveOptions{Backend: BackendAnthropic, UseTokens: true, Filter: "claude-3-5", Refresh: true})
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	if _, ok := hasModelID(withFilter, "claude-3-5-sonnet-20241022"); !ok {
		t.Fatal("explicit filter should reveal the legacy id")
	}
}

func TestResolveModels_PreferredOpenAIVariantsRemainVisible(t *testing.T) {
	stubLiveFetcher(t, func(b Backend) ([]ModelDef, error) {
		if b != BackendOpenAI {
			return nil, nil
		}
		return []ModelDef{
			{ID: "gpt-5.6-sol", Backend: BackendOpenAI},
			{ID: "gpt-5.6-terra", Backend: BackendOpenAI},
			{ID: "gpt-5.6-luna", Backend: BackendOpenAI},
			{ID: "gpt-5.5-pro", Backend: BackendOpenAI},
		}, nil
	})
	t.Setenv("OPENAI_API_KEY", "k")

	rows, err := ResolveModels(context.Background(), ResolveOptions{Backend: BackendOpenAI, UseTokens: true})
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	for _, id := range []string{"openai/gpt-5.6-sol", "openai/gpt-5.6-terra", "openai/gpt-5.6-luna"} {
		row, ok := hasModelID(rows, id)
		if !ok || !row.Live {
			t.Errorf("preferred API model %q = %+v, present=%v", id, row, ok)
		}
	}
	if _, ok := hasModelID(rows, "gpt-5.5-pro"); ok {
		t.Fatal("non-preferred API variant should remain hidden")
	}
}

func TestResolveModels_CacheWrittenAndReused(t *testing.T) {
	calls := 0
	stubLiveFetcher(t, func(b Backend) ([]ModelDef, error) {
		if b == BackendAnthropic {
			calls++
		}
		return nil, nil
	})
	t.Setenv("ANTHROPIC_API_KEY", "k") // after stub clears the key set

	opts := ResolveOptions{Backend: BackendAnthropic, UseTokens: true}
	if _, err := ResolveModels(context.Background(), opts); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	if calls != 1 {
		t.Fatalf("first resolve should fetch live once, got %d", calls)
	}

	// Second identical resolve must be served from the persisted cache.
	if _, err := ResolveModels(context.Background(), opts); err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if calls != 1 {
		t.Fatalf("second resolve should reuse cache (no fetch), got %d calls", calls)
	}

	// Refresh re-resolves.
	if _, err := ResolveModels(context.Background(), ResolveOptions{Backend: BackendAnthropic, UseTokens: true, Refresh: true}); err != nil {
		t.Fatalf("refresh resolve: %v", err)
	}
	if calls != 2 {
		t.Fatalf("refresh should re-fetch, got %d calls", calls)
	}
}
