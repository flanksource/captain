package captaintoken

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// stubStore counts lookups so a test can tell a cache hit from a miss.
type stubStore struct {
	record  Record
	err     error
	lookups atomic.Int64
}

func (s *stubStore) lookup(context.Context, string) (Record, error) {
	s.lookups.Add(1)
	if s.err != nil {
		return Record{}, s.err
	}
	return s.record, nil
}

// mintedRecord returns a token and the stored record that matches it.
func mintedRecord(t *testing.T, mutate func(*Record)) (string, Record) {
	t.Helper()
	minted, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		ID: minted.ID, SecretHash: minted.Hash,
		Name: "worker-01", Scope: ScopeGit, Agent: "worker-01",
	}
	if mutate != nil {
		mutate(&record)
	}
	return minted.Secret.Value(), record
}

func TestVerifyAcceptsALiveToken(t *testing.T) {
	raw, record := mintedRecord(t, nil)
	store := &stubStore{record: record}

	got, err := NewVerifier(store.lookup).Verify(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.Agent != "worker-01" || got.Scope != ScopeGit {
		t.Fatalf("record = %+v", got)
	}
}

// A durable token is the whole point: presenting it twice must keep working,
// unlike the single-use join token it replaces.
func TestVerifyIsReusable(t *testing.T) {
	raw, record := mintedRecord(t, nil)
	store := &stubStore{record: record}
	verifier := NewVerifier(store.lookup)

	for i := range 3 {
		if _, err := verifier.Verify(t.Context(), raw); err != nil {
			t.Fatalf("presentation %d failed: %v", i+1, err)
		}
	}
}

func TestVerifyRejectsRevokedAndExpired(t *testing.T) {
	revokedAt := time.Now().Add(-time.Minute)
	expiredAt := time.Now().Add(-time.Minute)

	tests := []struct {
		name   string
		mutate func(*Record)
		want   error
	}{
		{"revoked", func(r *Record) { r.RevokedAt = &revokedAt }, ErrRevoked},
		{"expired", func(r *Record) { r.ExpiresAt = &expiredAt }, ErrExpired},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, record := mintedRecord(t, tt.mutate)
			store := &stubStore{record: record}

			if _, err := NewVerifier(store.lookup).Verify(t.Context(), raw); !errors.Is(err, tt.want) {
				t.Fatalf("err = %v, want %v", err, tt.want)
			}
		})
	}
}

// A wrong secret and an unknown id must be indistinguishable to the caller.
func TestVerifyRejectsAWrongSecretAsUnknown(t *testing.T) {
	_, record := mintedRecord(t, nil)
	other, err := Mint()
	if err != nil {
		t.Fatal(err)
	}
	// Same id, different secret.
	forged := Prefix + "_" + record.ID + separator + "wrong-secret"
	store := &stubStore{record: record}

	if _, err := NewVerifier(store.lookup).Verify(t.Context(), forged); !errors.Is(err, ErrUnknown) {
		t.Fatalf("forged secret err = %v, want ErrUnknown", err)
	}
	_ = other
}

// The KDF must not run for an id that is not on file, or a flood of random
// credentials becomes a memory-and-CPU amplification attack.
func TestVerifyDoesNotHashAnUnknownID(t *testing.T) {
	store := &stubStore{err: ErrUnknown}
	verifier := NewVerifier(store.lookup)

	start := time.Now()
	for range 20 {
		if _, err := verifier.Verify(t.Context(), Prefix+"_unknownid.secret"); !errors.Is(err, ErrUnknown) {
			t.Fatalf("err = %v, want ErrUnknown", err)
		}
	}
	// 20 argon2 runs at ~19MiB each would take well over a second; an indexed
	// miss is microseconds. A generous bound still catches a regression that
	// moves hashing ahead of the lookup.
	if elapsed := time.Since(start); elapsed > 300*time.Millisecond {
		t.Fatalf("unknown ids took %s — the KDF is running before the lookup", elapsed)
	}
}

// A malformed credential must not reach the store at all.
func TestVerifyRejectsMalformedBeforeLookup(t *testing.T) {
	store := &stubStore{}
	if _, err := NewVerifier(store.lookup).Verify(t.Context(), "garbage"); !errors.Is(err, ErrMalformed) {
		t.Fatalf("err = %v, want ErrMalformed", err)
	}
	if store.lookups.Load() != 0 {
		t.Fatal("a malformed credential reached the store")
	}
}

func TestVerifyScope(t *testing.T) {
	raw, record := mintedRecord(t, func(r *Record) { r.Scope = ScopeGit })
	store := &stubStore{record: record}
	verifier := NewVerifier(store.lookup)

	if _, err := verifier.VerifyScope(t.Context(), raw, ScopeGit); err != nil {
		t.Fatal(err)
	}
	// A git-scoped agent token must not reach the API, which executes commands.
	_, err := verifier.VerifyScope(t.Context(), raw, ScopeAPI)
	if !errors.Is(err, ErrScope) {
		t.Fatalf("err = %v, want ErrScope", err)
	}
}

// The cache skips the KDF, never the liveness check — otherwise a revocation
// would not take effect until the entry aged out.
func TestCacheSkipsHashingButNotRevocation(t *testing.T) {
	raw, record := mintedRecord(t, nil)
	store := &stubStore{record: record}
	verifier := NewVerifier(store.lookup)

	if _, err := verifier.Verify(t.Context(), raw); err != nil {
		t.Fatal(err)
	}

	// Second presentation is a cache hit: it must still consult the store.
	before := store.lookups.Load()
	if _, err := verifier.Verify(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	if store.lookups.Load() != before+1 {
		t.Fatal("a cache hit skipped the store read, so revocation would be delayed")
	}

	// Revoke between presentations; the very next call must fail.
	revokedAt := time.Now()
	store.record.RevokedAt = &revokedAt
	if _, err := verifier.Verify(t.Context(), raw); !errors.Is(err, ErrRevoked) {
		t.Fatalf("err = %v, want ErrRevoked immediately after revocation", err)
	}
}

func TestCacheExpires(t *testing.T) {
	raw, record := mintedRecord(t, nil)
	store := &stubStore{record: record}
	verifier := NewVerifier(store.lookup)

	clock := time.Now()
	verifier.now = func() time.Time { return clock }
	verifier.ttl = time.Minute

	if _, err := verifier.Verify(t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	presented, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !verifier.cached(presented.CacheKey()) {
		t.Fatal("a verified token was not cached")
	}

	clock = clock.Add(2 * time.Minute)
	if verifier.cached(presented.CacheKey()) {
		t.Fatal("the cache entry outlived its TTL")
	}
}

// A stream of distinct valid credentials must not grow the cache without bound.
func TestCacheIsBounded(t *testing.T) {
	verifier := NewVerifier(func(context.Context, string) (Record, error) { return Record{}, ErrUnknown })
	verifier.maxKeys = 8

	for i := range 100 {
		verifier.remember(string(rune('a'+i%26)) + string(rune('a'+i/26)))
	}
	if len(verifier.cache) > verifier.maxKeys {
		t.Fatalf("cache holds %d entries, cap is %d", len(verifier.cache), verifier.maxKeys)
	}
}

func TestRecordActive(t *testing.T) {
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)

	if err := (Record{}).Active(now); err != nil {
		t.Fatalf("a token with no expiry or revocation should be active, got %v", err)
	}
	if err := (Record{ExpiresAt: &future}).Active(now); err != nil {
		t.Fatalf("a token expiring later should be active, got %v", err)
	}
	if err := (Record{ExpiresAt: &past}).Active(now); !errors.Is(err, ErrExpired) {
		t.Fatalf("err = %v, want ErrExpired", err)
	}
	if err := (Record{RevokedAt: &past}).Active(now); !errors.Is(err, ErrRevoked) {
		t.Fatalf("err = %v, want ErrRevoked", err)
	}
	// Revocation outranks expiry: it is the more specific reason.
	if err := (Record{RevokedAt: &past, ExpiresAt: &past}).Active(now); !errors.Is(err, ErrRevoked) {
		t.Fatalf("err = %v, want ErrRevoked", err)
	}
}
