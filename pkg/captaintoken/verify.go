package captaintoken

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

var (
	// ErrUnknown is returned for a token id that is not on file. It is
	// deliberately indistinguishable, to a caller, from a wrong secret.
	ErrUnknown = errors.New("unknown captain token")
	// ErrRevoked and ErrExpired are separated from ErrUnknown because a
	// legitimate holder of a real credential benefits from knowing which.
	ErrRevoked = errors.New("captain token has been revoked")
	ErrExpired = errors.New("captain token has expired")
	// ErrScope means the credential is real but not permitted here.
	ErrScope = errors.New("captain token does not carry the required scope")
)

// Record is a stored token, as the verifier needs to see it. The store owns
// persistence; this package owns the credential arithmetic.
type Record struct {
	ID         string
	SecretHash string
	Name       string
	Scope      Scope
	Agent      string
	Pool       bool
	PoolAgents []string
	MaxAgents  int
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
}

// Active reports why a token cannot be used, or nil.
func (r Record) Active(now time.Time) error {
	if r.RevokedAt != nil {
		return ErrRevoked
	}
	if r.ExpiresAt != nil && !now.Before(*r.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

// Lookup fetches a token by its public id. It returns ErrUnknown when there is
// no such row.
type Lookup func(ctx context.Context, id string) (Record, error)

// Verifier turns a presented credential into a Record.
//
// The order of operations is the load-bearing part. The id is looked up first,
// on an index, and the KDF runs only for an id that exists — so a flood of
// random credentials costs an indexed miss each rather than 19 MiB and ~60ms of
// argon2. Only an attacker who already knows a real token id can force the
// expensive path, and the cache below bounds even that.
type Verifier struct {
	lookup Lookup
	ttl    time.Duration
	now    func() time.Time

	mu      sync.Mutex
	cache   map[string]time.Time
	maxKeys int
}

// DefaultCacheTTL bounds how long a successful KDF verification is trusted
// without re-running. Revocation is still prompt: the record is re-read from
// the store on every request even on a cache hit, so only the argon2 step is
// skipped, never the revoked/expired check.
const DefaultCacheTTL = 30 * time.Second

// maxCacheKeys caps the cache so a stream of distinct valid-id credentials
// cannot grow it without bound.
const maxCacheKeys = 1024

// NewVerifier builds a verifier over a store lookup.
func NewVerifier(lookup Lookup) *Verifier {
	return &Verifier{
		lookup:  lookup,
		ttl:     DefaultCacheTTL,
		now:     time.Now,
		cache:   map[string]time.Time{},
		maxKeys: maxCacheKeys,
	}
}

// Verify resolves a raw credential to its record, checking that it exists, that
// its secret matches, and that it is neither revoked nor expired.
func (v *Verifier) Verify(ctx context.Context, raw string) (Record, error) {
	presented, err := Parse(raw)
	if err != nil {
		return Record{}, err
	}
	record, err := v.lookup(ctx, presented.ID)
	if err != nil {
		return Record{}, err
	}
	// Liveness is checked against the freshly read record on every request,
	// cache hit or not, so a revocation takes effect immediately (R8.5).
	if err := record.Active(v.now()); err != nil {
		return Record{}, err
	}
	if !v.verifySecret(presented, record.SecretHash) {
		return Record{}, ErrUnknown
	}
	return record, nil
}

// VerifyScope resolves a credential and requires a scope.
func (v *Verifier) VerifyScope(ctx context.Context, raw string, want Scope) (Record, error) {
	record, err := v.Verify(ctx, raw)
	if err != nil {
		return Record{}, err
	}
	if record.Scope != want {
		return Record{}, fmt.Errorf("%w: token %q has scope %q, not %q", ErrScope, record.ID, record.Scope, want)
	}
	return record, nil
}

// verifySecret runs the KDF unless a recent identical presentation already
// passed. The cache key is a digest, so the map never holds a secret.
func (v *Verifier) verifySecret(presented Presented, storedHash string) bool {
	key := presented.CacheKey()
	if v.cached(key) {
		return true
	}
	if !presented.Verify(storedHash) {
		return false
	}
	v.remember(key)
	return true
}

func (v *Verifier) cached(key string) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	expires, ok := v.cache[key]
	if !ok {
		return false
	}
	if !v.now().Before(expires) {
		delete(v.cache, key)
		return false
	}
	return true
}

func (v *Verifier) remember(key string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.cache) >= v.maxKeys {
		v.evictExpiredLocked()
		// Still full of live entries: drop the cache rather than grow past the
		// cap. Verification stays correct, it just costs the KDF again.
		if len(v.cache) >= v.maxKeys {
			v.cache = map[string]time.Time{}
		}
	}
	v.cache[key] = v.now().Add(v.ttl)
}

func (v *Verifier) evictExpiredLocked() {
	now := v.now()
	for key, expires := range v.cache {
		if !now.Before(expires) {
			delete(v.cache, key)
		}
	}
}
