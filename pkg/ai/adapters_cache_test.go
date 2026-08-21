package ai

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/credentials"
)

func TestCachedAdaptersReusesWithinTTL(t *testing.T) {
	prevProbe := adapterProbe
	prevAuthProbe := adapterAuthProbe
	prevCache, prevAt, prevFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
	t.Cleanup(func() {
		adapterProbe = prevProbe
		adapterAuthProbe = prevAuthProbe
		adapterCache, adapterCacheAt, adapterCacheFingerprint = prevCache, prevAt, prevFingerprint
	})
	probe := fakeProbe(nil, nil, nil, t.TempDir())
	adapterAuthProbe = func() AuthProbe { return probe }

	stub := []AdapterStatus{{Backend: string(BackendAnthropic), Type: "api"}}
	calls := 0
	adapterProbe = func(AuthProbe) ([]AdapterStatus, error) {
		calls++
		return stub, nil
	}
	adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""

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

func TestCachedAdaptersUsesMetadataForCacheHits(t *testing.T) {
	prevProbe, prevAuthProbe := adapterProbe, adapterAuthProbe
	prevCache, prevAt, prevFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
	t.Cleanup(func() {
		adapterProbe, adapterAuthProbe = prevProbe, prevAuthProbe
		adapterCache, adapterCacheAt, adapterCacheFingerprint = prevCache, prevAt, prevFingerprint
	})
	adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""

	home := t.TempDir()
	authFile := filepath.Join(home, ".codex", "auth.json")
	metadataIdentity := "mtime-a"
	contentIdentity := "content-a"
	hashReads := 0
	adapterAuthProbe = func() AuthProbe {
		probe := fakeProbe(nil, nil, map[string]bool{authFile: true}, home)
		probe.FileMetadataIdentity = func(path string) string {
			if path == authFile {
				return metadataIdentity
			}
			return ""
		}
		probe.FileIdentity = func(path string) string {
			if path == authFile {
				hashReads++
				return contentIdentity
			}
			return ""
		}
		return probe
	}
	calls := 0
	adapterProbe = func(probe AuthProbe) ([]AdapterStatus, error) {
		calls++
		return []AdapterStatus{{Backend: string(BackendCodexCLI), Models: []string{probe.FileIdentity(authFile)}}}, nil
	}

	base := time.Unix(1_500_000, 0)
	got, err := CachedAdapters(base)
	if err != nil || got[0].Models[0] != "content-a" {
		t.Fatalf("initial adapters = %+v err=%v", got, err)
	}
	if hashReads != 2 {
		t.Fatalf("initial credential hashes = %d, want 2", hashReads)
	}
	if _, err := CachedAdapters(base.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if hashReads != 2 || calls != 1 {
		t.Fatalf("cache hit: credential hashes=%d adapter probes=%d, want 2 and 1", hashReads, calls)
	}

	metadataIdentity = "mtime-b"
	contentIdentity = "content-b"
	got, err = CachedAdapters(base.Add(2 * time.Second))
	if err != nil || got[0].Models[0] != "content-b" {
		t.Fatalf("changed credentials adapters = %+v err=%v", got, err)
	}
	if hashReads != 4 || calls != 2 {
		t.Fatalf("invalidated cache: credential hashes=%d adapter probes=%d, want 4 and 2", hashReads, calls)
	}
}

func TestCachedAdaptersDoesNotCacheErrors(t *testing.T) {
	prevProbe := adapterProbe
	prevAuthProbe := adapterAuthProbe
	prevCache, prevAt, prevFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
	t.Cleanup(func() {
		adapterProbe = prevProbe
		adapterAuthProbe = prevAuthProbe
		adapterCache, adapterCacheAt, adapterCacheFingerprint = prevCache, prevAt, prevFingerprint
	})
	probe := fakeProbe(nil, nil, nil, t.TempDir())
	adapterAuthProbe = func() AuthProbe { return probe }
	adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""

	calls := 0
	adapterProbe = func(AuthProbe) ([]AdapterStatus, error) {
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

func TestCachedAdaptersInvalidatesCredentialSourceTokenAndEndpointImmediately(t *testing.T) {
	prevProbe, prevAuthProbe := adapterProbe, adapterAuthProbe
	prevCache, prevAt, prevFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
	t.Cleanup(func() {
		adapterProbe, adapterAuthProbe = prevProbe, prevAuthProbe
		adapterCache, adapterCacheAt, adapterCacheFingerprint = prevCache, prevAt, prevFingerprint
	})
	adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""

	token := "same-effective-token"
	source := credentials.SourceVault
	detail := "vault"
	apiURL := "https://one.example/v1"
	home := t.TempDir()
	adapterAuthProbe = func() AuthProbe {
		probe := fakeProbe(nil, nil, nil, home)
		probe.APICredentials = map[Backend]api.ResolvedAPIKey{
			BackendOpenAI: {Token: token, Source: source, Detail: detail},
		}
		probe.APIURLs = map[Backend]string{BackendOpenAI: apiURL}
		return probe
	}
	calls := 0
	adapterProbe = func(probe AuthProbe) ([]AdapterStatus, error) {
		calls++
		return []AdapterStatus{resolveAdapterFrozen(BackendOpenAI, probe)}, nil
	}

	base := time.Unix(3_000_000, 0)
	got, err := CachedAdapters(base)
	if err != nil || len(got) != 1 || got[0].AuthMethod != "Captain vault" {
		t.Fatalf("initial adapters = %+v err=%v", got, err)
	}
	if _, err := CachedAdapters(base.Add(time.Second)); err != nil || calls != 1 {
		t.Fatalf("unchanged cache: calls=%d err=%v", calls, err)
	}

	// The effective token is unchanged, but auth reporting must immediately move
	// from vault to environment.
	source, detail = credentials.SourceEnvironment, "OPENAI_API_KEY"
	got, err = CachedAdapters(base.Add(2 * time.Second))
	if err != nil || got[0].AuthMethod != "OPENAI_API_KEY (env)" || calls != 2 {
		t.Fatalf("source change adapters = %+v calls=%d err=%v", got, calls, err)
	}
	apiURL = "https://two.example/v1"
	if _, err := CachedAdapters(base.Add(3 * time.Second)); err != nil || calls != 3 {
		t.Fatalf("endpoint change: calls=%d err=%v", calls, err)
	}
	token = "rotated-effective-token"
	if _, err := CachedAdapters(base.Add(4 * time.Second)); err != nil || calls != 4 {
		t.Fatalf("token rotation: calls=%d err=%v", calls, err)
	}
	token = ""
	got, err = CachedAdapters(base.Add(5 * time.Second))
	if err != nil || got[0].Authenticated || calls != 5 {
		t.Fatalf("token removal adapters = %+v calls=%d err=%v", got, calls, err)
	}
}

func TestCachedAdaptersInvalidatesLocalLoginIdentityImmediately(t *testing.T) {
	prevProbe, prevAuthProbe := adapterProbe, adapterAuthProbe
	prevCache, prevAt, prevFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
	t.Cleanup(func() {
		adapterProbe, adapterAuthProbe = prevProbe, prevAuthProbe
		adapterCache, adapterCacheAt, adapterCacheFingerprint = prevCache, prevAt, prevFingerprint
	})
	adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""

	home := t.TempDir()
	authFile := filepath.Join(home, ".codex", "auth.json")
	accountIdentity := "account-a"
	adapterAuthProbe = func() AuthProbe {
		probe := fakeProbe(nil, map[string]string{"codex": "/bin/codex"}, map[string]bool{authFile: true}, home)
		probe.FileIdentity = func(path string) string {
			if path == authFile {
				return accountIdentity
			}
			return ""
		}
		return probe
	}
	calls := 0
	adapterProbe = func(probe AuthProbe) ([]AdapterStatus, error) {
		calls++
		status := resolveAdapterFrozen(BackendCodexCLI, probe)
		status.Models = []string{probe.FileIdentity(authFile)}
		return []AdapterStatus{status}, nil
	}

	base := time.Unix(4_000_000, 0)
	got, err := CachedAdapters(base)
	if err != nil || got[0].Models[0] != "account-a" || calls != 1 {
		t.Fatalf("initial local account = %+v calls=%d err=%v", got, calls, err)
	}
	accountIdentity = "account-b"
	got, err = CachedAdapters(base.Add(time.Second))
	if err != nil || got[0].Models[0] != "account-b" || calls != 2 {
		t.Fatalf("changed local account = %+v calls=%d err=%v", got, calls, err)
	}
}

func TestCachedAdaptersReturnsDeepCopies(t *testing.T) {
	prevProbe, prevAuthProbe := adapterProbe, adapterAuthProbe
	prevCache, prevAt, prevFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
	t.Cleanup(func() {
		adapterProbe, adapterAuthProbe = prevProbe, prevAuthProbe
		adapterCache, adapterCacheAt, adapterCacheFingerprint = prevCache, prevAt, prevFingerprint
	})
	adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""
	probe := fakeProbe(nil, nil, nil, t.TempDir())
	adapterAuthProbe = func() AuthProbe { return probe }
	adapterProbe = func(AuthProbe) ([]AdapterStatus, error) {
		return []AdapterStatus{{
			Backend: string(BackendOpenAI), Models: []string{"model-a"},
			ModelDetails: []ModelDef{{
				ID: "model-a", InputMediaTypes: []string{"image/png"}, SupportedEfforts: []api.Effort{api.EffortLow},
			}},
		}}, nil
	}

	base := time.Unix(5_000_000, 0)
	first, err := CachedAdapters(base)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Backend = "poisoned"
	first[0].Models[0] = "poisoned"
	first[0].ModelDetails[0].ID = "poisoned"
	first[0].ModelDetails[0].InputMediaTypes[0] = "poisoned"
	first[0].ModelDetails[0].SupportedEfforts[0] = api.EffortHigh

	second, err := CachedAdapters(base.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Backend != string(BackendOpenAI) || second[0].Models[0] != "model-a" || second[0].ModelDetails[0].ID != "model-a" || second[0].ModelDetails[0].InputMediaTypes[0] != "image/png" || second[0].ModelDetails[0].SupportedEfforts[0] != api.EffortLow {
		t.Fatalf("cached adapters were mutated through a returned value: %+v", second)
	}
}

func TestCachedAdaptersReturnsFreshestUnsettledProbeState(t *testing.T) {
	prevProbe, prevAuthProbe := adapterProbe, adapterAuthProbe
	prevCache, prevAt, prevFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
	t.Cleanup(func() {
		adapterProbe, adapterAuthProbe = prevProbe, prevAuthProbe
		adapterCache, adapterCacheAt, adapterCacheFingerprint = prevCache, prevAt, prevFingerprint
	})
	adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""

	home := t.TempDir()
	captures := 0
	adapterAuthProbe = func() AuthProbe {
		captures++
		probe := fakeProbe(nil, nil, nil, home)
		probe.APICredentials = map[Backend]api.ResolvedAPIKey{
			BackendOpenAI: {Token: string(rune('a' + captures))},
		}
		return probe
	}
	adapterProbe = func(probe AuthProbe) ([]AdapterStatus, error) {
		return []AdapterStatus{{
			Backend: string(BackendOpenAI),
			Models:  []string{probe.credentials.APIKey(BackendOpenAI).Token},
		}}, nil
	}

	got, err := CachedAdapters(time.Unix(6_000_000, 0))
	if !errors.Is(err, ErrAdapterProbeUnsettled) {
		t.Fatalf("unsettled probe error = %v, want ErrAdapterProbeUnsettled", err)
	}
	if len(got) != 1 || len(got[0].Models) != 1 || got[0].Models[0] != "c" {
		t.Fatalf("unsettled probe adapters = %+v, want freshest observation", got)
	}
	if adapterCache != nil {
		t.Fatalf("unsettled probe published cache: %+v", adapterCache)
	}
}

func TestCachedAdaptersRejectsRecaptureError(t *testing.T) {
	prevProbe, prevAuthProbe := adapterProbe, adapterAuthProbe
	prevCache, prevAt, prevFingerprint := adapterCache, adapterCacheAt, adapterCacheFingerprint
	t.Cleanup(func() {
		adapterProbe, adapterAuthProbe = prevProbe, prevAuthProbe
		adapterCache, adapterCacheAt, adapterCacheFingerprint = prevCache, prevAt, prevFingerprint
	})
	adapterCache, adapterCacheAt, adapterCacheFingerprint = nil, time.Time{}, ""

	wantErr := errors.New("credential vault became unreadable")
	captures := 0
	adapterAuthProbe = func() AuthProbe {
		captures++
		probe := fakeProbe(nil, nil, nil, t.TempDir())
		if captures > 1 {
			probe.ProbeError = wantErr
		}
		return probe
	}
	adapterProbe = func(AuthProbe) ([]AdapterStatus, error) {
		return []AdapterStatus{{Backend: string(BackendOpenAI)}}, nil
	}

	got, err := CachedAdapters(time.Unix(7_000_000, 0))
	if !errors.Is(err, wantErr) || got != nil {
		t.Fatalf("recapture error returned adapters=%+v err=%v", got, err)
	}
}
