package ai

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credentials"
)

func TestResolveFingerprintChangesWhenVaultTokenRotates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	credentials.SetPathForTesting(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { credentials.SetPathForTesting("") })
	t.Setenv("OPENAI_API_KEY", "")
	vault, err := credentials.DefaultVault()
	if err != nil {
		t.Fatalf("DefaultVault: %v", err)
	}
	if err := vault.Set("openai", "first-secret-token"); err != nil {
		t.Fatalf("Set first token: %v", err)
	}
	firstCredentials, err := credentialSnapshotForOptions(ResolveOptions{Provider: OpenAI, Mode: ModeAPI, UseTokens: true})
	if err != nil {
		t.Fatalf("first credential snapshot: %v", err)
	}
	first, cacheable, err := resolveFingerprint(ResolveOptions{Provider: OpenAI, Mode: ModeAPI, UseTokens: true}, firstCredentials)
	if err != nil || !cacheable {
		t.Fatalf("first fingerprint: cacheable=%v err=%v", cacheable, err)
	}
	if err := vault.Set("openai", "second-secret-token"); err != nil {
		t.Fatalf("Set second token: %v", err)
	}
	secondCredentials, err := credentialSnapshotForOptions(ResolveOptions{Provider: OpenAI, Mode: ModeAPI, UseTokens: true})
	if err != nil {
		t.Fatalf("second credential snapshot: %v", err)
	}
	second, cacheable, err := resolveFingerprint(ResolveOptions{Provider: OpenAI, Mode: ModeAPI, UseTokens: true}, secondCredentials)
	if err != nil || !cacheable {
		t.Fatalf("second fingerprint: cacheable=%v err=%v", cacheable, err)
	}
	if first == second {
		t.Fatal("token rotation must invalidate the model cache fingerprint")
	}
	if strings.Contains(first+second, "secret-token") {
		t.Fatalf("fingerprints expose token material: %q %q", first, second)
	}
}

// stubLiveFetcher installs a fake live-model fetcher for the test and restores
// the real one on cleanup. It also isolates API-key env so only the backends
// the test opts into are fetched, and points the model cache at a temp HOME.
func stubLiveFetcher(t *testing.T, fn func(p *ModelProvider) ([]ModelDef, error)) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	prev := liveModelFetcher
	liveModelFetcher = func(_ context.Context, p *ModelProvider, _, _ string) ([]ModelDef, error) { return fn(p) }
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
	stubLiveFetcher(t, func(p *ModelProvider) ([]ModelDef, error) {
		t.Fatalf("live fetch must not run without tokens (provider %s)", p.Name)
		return nil, nil
	})

	rows, err := ResolveModels(context.Background(), ResolveOptions{Provider: Anthropic, Mode: ModeAPI, UseTokens: false})
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("expected catalog anthropic models")
	}
	for _, r := range rows {
		if r.Provider != Anthropic || r.Mode != ModeAPI {
			t.Fatalf("runtime filter leaked: %s %s", r.Provider.Name, r.Mode)
		}
		if r.Live {
			t.Fatalf("no row should be Live without tokens: %q", r.ID)
		}
	}
}

func TestResolveModels_TokensUnionDedup(t *testing.T) {
	stubLiveFetcher(t, func(p *ModelProvider) ([]ModelDef, error) {
		if p != Anthropic {
			return nil, nil
		}
		return []ModelDef{
			{ID: "claude-sonnet-5", Provider: Anthropic.Name, Mode: ModeAPI}, // dedups with catalog anthropic/claude-sonnet-5
			{ID: "claude-new-xyz", Provider: Anthropic.Name, Mode: ModeAPI},  // net-new live model
		}, nil
	})
	t.Setenv("ANTHROPIC_API_KEY", "k") // after stub clears the key set

	rows, err := ResolveModels(context.Background(), ResolveOptions{Provider: Anthropic, Mode: ModeAPI, UseTokens: true})
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

	// No duplicate (runtime, bareID) for claude-sonnet-5.
	bareCount := 0
	for _, r := range rows {
		if r.Provider == Anthropic && r.Mode == ModeAPI && r.BareID() == "claude-sonnet-5" {
			bareCount++
		}
	}
	if bareCount != 1 {
		t.Fatalf("claude-sonnet-5 appears %d times, want deduped to 1", bareCount)
	}
}

func TestResolveModels_LiveErrorFailsLoud(t *testing.T) {
	stubLiveFetcher(t, func(p *ModelProvider) ([]ModelDef, error) {
		return nil, errors.New("boom")
	})
	t.Setenv("ANTHROPIC_API_KEY", "k") // after stub clears the key set

	if _, err := ResolveModels(context.Background(), ResolveOptions{Provider: Anthropic, Mode: ModeAPI, UseTokens: true}); err == nil {
		t.Fatal("expected live fetch error to propagate (fail loud)")
	}
}

func TestResolveModels_LegacyHiddenUnlessFiltered(t *testing.T) {
	stubLiveFetcher(t, func(p *ModelProvider) ([]ModelDef, error) {
		if p != Anthropic {
			return nil, nil
		}
		return []ModelDef{{ID: "claude-3-5-sonnet-20241022", Provider: Anthropic.Name, Mode: ModeAPI}}, nil
	})
	t.Setenv("ANTHROPIC_API_KEY", "k") // after stub clears the key set

	noFilter, err := ResolveModels(context.Background(), ResolveOptions{Provider: Anthropic, Mode: ModeAPI, UseTokens: true})
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	if _, ok := hasModelID(noFilter, "claude-3-5-sonnet-20241022"); ok {
		t.Fatal("legacy id should be hidden without a filter")
	}

	withFilter, err := ResolveModels(context.Background(), ResolveOptions{Provider: Anthropic, Mode: ModeAPI, UseTokens: true, Filter: "claude-3-5", Refresh: true})
	if err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	if _, ok := hasModelID(withFilter, "claude-3-5-sonnet-20241022"); !ok {
		t.Fatal("explicit filter should reveal the legacy id")
	}
}

func TestResolveModels_PreferredOpenAIVariantsRemainVisible(t *testing.T) {
	stubLiveFetcher(t, func(p *ModelProvider) ([]ModelDef, error) {
		if p != OpenAI {
			return nil, nil
		}
		return []ModelDef{
			{ID: "gpt-5.6-sol", Provider: OpenAI.Name, Mode: ModeAPI},
			{ID: "gpt-5.6-terra", Provider: OpenAI.Name, Mode: ModeAPI},
			{ID: "gpt-5.6-luna", Provider: OpenAI.Name, Mode: ModeAPI},
			{ID: "gpt-5.5-pro", Provider: OpenAI.Name, Mode: ModeAPI},
		}, nil
	})
	t.Setenv("OPENAI_API_KEY", "k")

	rows, err := ResolveModels(context.Background(), ResolveOptions{Provider: OpenAI, Mode: ModeAPI, UseTokens: true})
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
	stubLiveFetcher(t, func(p *ModelProvider) ([]ModelDef, error) {
		if p == Anthropic {
			calls++
		}
		return nil, nil
	})
	t.Setenv("ANTHROPIC_API_KEY", "k") // after stub clears the key set

	opts := ResolveOptions{Provider: Anthropic, Mode: ModeAPI, UseTokens: true}
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
	if _, err := ResolveModels(context.Background(), ResolveOptions{Provider: Anthropic, Mode: ModeAPI, UseTokens: true, Refresh: true}); err != nil {
		t.Fatalf("refresh resolve: %v", err)
	}
	if calls != 2 {
		t.Fatalf("refresh should re-fetch, got %d calls", calls)
	}
}

func TestResolveModelsCredentialCacheIsolationAndSourceConsistency(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := liveModelFetcher
	calls := map[string]int{}
	liveModelFetcher = func(_ context.Context, p *ModelProvider, token, _ string) ([]ModelDef, error) {
		if p != OpenAI {
			t.Fatalf("unexpected live provider %s", p.Name)
		}
		calls[token]++
		modelID := map[string]string{"token-a": "private-model-a", "token-b": "private-model-b"}[token]
		return []ModelDef{{ID: modelID, Provider: p.Name, Mode: ModeAPI}}, nil
	}
	t.Cleanup(func() { liveModelFetcher = prev })

	resolve := func(openAIToken, source, unrelatedToken string) []ResolvedModel {
		t.Helper()
		rows, err := ResolveModels(context.Background(), ResolveOptions{
			Provider:  OpenAI,
			Mode:      ModeAPI,
			UseTokens: true,
			Credentials: NewCredentialSnapshot(map[string]api.ResolvedAPIKey{
				OpenAI.Name:    {Token: openAIToken, Source: source, Detail: source},
				Anthropic.Name: {Token: unrelatedToken, Source: source, Detail: source},
			}),
		})
		if err != nil {
			t.Fatalf("ResolveModels(%s): %v", openAIToken, err)
		}
		return rows
	}

	if _, ok := hasModelID(resolve("token-b", credentials.SourceEnvironment, "unrelated-a"), "private-model-b"); !ok {
		t.Fatal("token B availability missing")
	}
	// Source and unrelated-provider changes must reuse the same OpenAI entry.
	if _, ok := hasModelID(resolve("token-b", credentials.SourceVault, "unrelated-b"), "private-model-b"); !ok {
		t.Fatal("same effective token should reuse availability across sources")
	}
	if _, ok := hasModelID(resolve("token-a", credentials.SourceEnvironment, "unrelated-b"), "private-model-a"); !ok {
		t.Fatal("token A availability missing")
	}
	if _, ok := hasModelID(resolve("token-b", credentials.SourceEnvironment, "unrelated-c"), "private-model-b"); !ok {
		t.Fatal("token B cache should survive resolving token A")
	}
	if calls["token-a"] != 1 || calls["token-b"] != 1 {
		t.Fatalf("live calls = %v, want one isolated fetch per effective OpenAI token", calls)
	}
}

func TestResolveModelsEndpointIsolation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := liveModelFetcher
	var endpoints []string
	liveModelFetcher = func(_ context.Context, p *ModelProvider, _ string, endpoint string) ([]ModelDef, error) {
		endpoints = append(endpoints, endpoint)
		return []ModelDef{{ID: fmt.Sprintf("endpoint-%d", len(endpoints)), Provider: p.Name, Mode: ModeAPI}}, nil
	}
	t.Cleanup(func() { liveModelFetcher = prev })
	credentials := NewCredentialSnapshot(map[string]api.ResolvedAPIKey{
		OpenAI.Name: {Token: "endpoint-token"},
	})

	for _, apiURL := range []string{
		"https://tenant-one.example/v1?account=private-one",
		"https://tenant-one.example/v1?account=private-one",
		"https://tenant-two.example/v1?account=private-two",
	} {
		if _, err := ResolveModels(context.Background(), ResolveOptions{
			Provider: OpenAI, Mode: ModeAPI, UseTokens: true, Credentials: credentials, APIURL: apiURL,
		}); err != nil {
			t.Fatalf("ResolveModels(%s): %v", apiURL, err)
		}
	}
	if len(endpoints) != 2 {
		t.Fatalf("live endpoints = %v, want one fetch per exact endpoint", endpoints)
	}
	if endpoints[0] != "https://tenant-one.example/v1/models?account=private-one" || endpoints[1] != "https://tenant-two.example/v1/models?account=private-two" {
		t.Fatalf("resolved endpoints = %v", endpoints)
	}
	entries, err := os.ReadDir(filepath.Join(os.Getenv("HOME"), ".config", "captain", "models"))
	if err != nil {
		t.Fatalf("ReadDir model cache: %v", err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(os.Getenv("HOME"), ".config", "captain", "models", entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "private-one") || strings.Contains(string(data), "private-two") {
			t.Fatalf("cache %s persisted a plaintext endpoint identifier", entry.Name())
		}
	}
}

func TestResolveModelsConcurrentProvidersRetainSecureIndependentEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	root := filepath.Join(home, ".config", "captain")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(root, "models.json")
	if err := os.WriteFile(legacy, []byte(`{"models":[{"id":"legacy-private"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	prev := liveModelFetcher
	var mu sync.Mutex
	calls := map[string]int{}
	liveModelFetcher = func(_ context.Context, p *ModelProvider, _, _ string) ([]ModelDef, error) {
		mu.Lock()
		calls[p.Name]++
		mu.Unlock()
		return []ModelDef{{ID: p.Name + "-private", Provider: p.Name, Mode: ModeAPI}}, nil
	}
	t.Cleanup(func() { liveModelFetcher = prev })

	options := []ResolveOptions{
		{Provider: OpenAI, Mode: ModeAPI, UseTokens: true, Credentials: NewCredentialSnapshot(map[string]api.ResolvedAPIKey{OpenAI.Name: {Token: "openai-secret-token"}})},
		{Provider: Anthropic, Mode: ModeAPI, UseTokens: true, Credentials: NewCredentialSnapshot(map[string]api.ResolvedAPIKey{Anthropic.Name: {Token: "anthropic-secret-token"}})},
	}
	runConcurrent := func(options []ResolveOptions) {
		t.Helper()
		var wg sync.WaitGroup
		errs := make(chan error, len(options))
		for _, opts := range options {
			wg.Add(1)
			go func(opts ResolveOptions) {
				defer wg.Done()
				_, err := ResolveModels(context.Background(), opts)
				errs <- err
			}(opts)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent ResolveModels: %v", err)
			}
		}
	}
	runConcurrent(options)
	runConcurrent(options)
	if calls[OpenAI.Name] != 1 || calls[Anthropic.Name] != 1 {
		t.Fatalf("warm-cache calls = %v, want one fetch per p", calls)
	}
	refresh := options[0]
	refresh.Refresh = true
	if _, err := ResolveModels(context.Background(), refresh); err != nil {
		t.Fatalf("refresh OpenAI: %v", err)
	}
	if _, err := ResolveModels(context.Background(), options[1]); err != nil {
		t.Fatalf("warm Anthropic after OpenAI refresh: %v", err)
	}
	if calls[OpenAI.Name] != 2 || calls[Anthropic.Name] != 1 {
		t.Fatalf("refresh calls = %v, want refresh isolated to OpenAI", calls)
	}

	assertMode := func(path string, want os.FileMode) {
		t.Helper()
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("mode %s = %04o, want %04o", path, got, want)
		}
	}
	assertMode(root, 0o700)
	assertMode(filepath.Join(root, "models"), 0o700)
	assertMode(filepath.Join(root, "model-cache.key"), 0o600)
	assertMode(legacy, 0o600)
	entries, err := os.ReadDir(filepath.Join(root, "models"))
	if err != nil {
		t.Fatal(err)
	}
	jsonEntries := 0
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temporary cache file remains: %s", entry.Name())
		}
		path := filepath.Join(root, "models", entry.Name())
		assertMode(path, 0o600)
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		jsonEntries++
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "openai-secret-token") || strings.Contains(string(data), "anthropic-secret-token") {
			t.Fatalf("cache %s persisted raw credential material", entry.Name())
		}
	}
	if jsonEntries != 2 {
		t.Fatalf("cache entries = %v, want one JSON entry per p", entries)
	}
}

func TestResolveModelsSerializesSameCredentialCacheEntry(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	previous := liveModelFetcher
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	var mu sync.Mutex
	liveModelFetcher = func(_ context.Context, p *ModelProvider, _, _ string) ([]ModelDef, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			close(started)
			<-release
		}
		return []ModelDef{{ID: fmt.Sprintf("private-model-%d", call), Provider: p.Name, Mode: ModeAPI}}, nil
	}
	t.Cleanup(func() { liveModelFetcher = previous })
	opts := ResolveOptions{
		Provider: OpenAI, Mode: ModeAPI, UseTokens: true,
		Credentials: NewCredentialSnapshot(map[string]api.ResolvedAPIKey{OpenAI.Name: {Token: "shared-token"}}),
	}

	firstDone := make(chan error, 1)
	go func() {
		_, err := ResolveModels(context.Background(), opts)
		firstDone <- err
	}()
	<-started
	secondDone := make(chan []ResolvedModel, 1)
	go func() {
		rows, _ := ResolveModels(context.Background(), opts)
		secondDone <- rows
	}()
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	rows := <-secondDone
	if _, ok := hasModelID(rows, "private-model-1"); !ok {
		t.Fatalf("second resolve did not reuse serialized cache entry: %+v", rows)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("live fetches = %d, want one for the shared cache identity", calls)
	}
}

func TestLockModelCacheHonorsContextWhileContended(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	unlock, err := lockModelCache(context.Background(), "contended")
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	unexpectedUnlock, err := lockModelCache(ctx, "contended")
	if unexpectedUnlock != nil {
		unexpectedUnlock()
		t.Fatal("contended lock was acquired")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended lock error = %v, want context deadline", err)
	}
}

func TestResolveModelsCatalogOnlyCreatesNoCredentialVerifier(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prev := liveModelFetcher
	liveModelFetcher = func(_ context.Context, p *ModelProvider, _, _ string) ([]ModelDef, error) {
		t.Fatalf("catalog-only resolution fetched %s", p.Name)
		return nil, nil
	}
	t.Cleanup(func() { liveModelFetcher = prev })
	if _, err := ResolveModels(context.Background(), ResolveOptions{
		Provider: Anthropic, Mode: ModeAPI, UseTokens: false,
		Credentials: NewCredentialSnapshot(map[string]api.ResolvedAPIKey{Anthropic.Name: {Token: "must-not-be-fingerprinted"}}),
	}); err != nil {
		t.Fatalf("ResolveModels: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".config", "captain", "model-cache.key")); !os.IsNotExist(err) {
		t.Fatalf("catalog-only resolution created a credential verifier: %v", err)
	}
}

func TestResolveModelsSecureKeyFailureFallsBackToUncachedLiveResolution(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prevFetcher := liveModelFetcher
	prevKey := modelCacheHMACKey
	calls := 0
	liveModelFetcher = func(_ context.Context, p *ModelProvider, _, _ string) ([]ModelDef, error) {
		calls++
		return []ModelDef{{ID: "uncached-private", Provider: p.Name, Mode: ModeAPI}}, nil
	}
	modelCacheHMACKey = func() ([]byte, error) { return nil, errors.New("unavailable") }
	t.Cleanup(func() {
		liveModelFetcher = prevFetcher
		modelCacheHMACKey = prevKey
	})
	opts := ResolveOptions{
		Provider: OpenAI, Mode: ModeAPI, UseTokens: true,
		Credentials: NewCredentialSnapshot(map[string]api.ResolvedAPIKey{OpenAI.Name: {Token: "still-usable-live"}}),
	}
	for i := 0; i < 2; i++ {
		if _, err := ResolveModels(context.Background(), opts); err != nil {
			t.Fatalf("ResolveModels attempt %d: %v", i+1, err)
		}
	}
	if calls != 2 {
		t.Fatalf("live calls = %d, want uncached fetch after each secure-key failure", calls)
	}
}
