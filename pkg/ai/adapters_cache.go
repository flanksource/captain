package ai

import (
	"sync"
	"time"
)

// adapterCacheTTL bounds how long a probed adapter set is reused. A long-running
// server (whoami-backed model menu, prompt schema) refreshes on this cadence so
// key/login/model changes surface without a probe per request.
const adapterCacheTTL = 60 * time.Second

// adapterProbe is the live probe sourcing the cache. It is a package var so
// tests can substitute a deterministic, network-free stub.
var adapterProbe = func() ([]AdapterStatus, error) {
	return ProbeAdapters(WhoamiOptions{Models: true}, OSAuthProbe())
}

var (
	adapterCacheMu sync.Mutex
	adapterCache   []AdapterStatus
	adapterCacheAt time.Time
)

// CachedAdapters returns the probed adapters, reusing a cached probe within the
// TTL. A probe error is never cached: the next call retries so a transient
// failure does not permanently empty the catalog. `now` is a parameter so tests
// can advance time deterministically.
//
// The cache stores the raw probe; the user's opt-out set is applied on the way
// out. Baking it in would make a toggle wait out the TTL.
func CachedAdapters(now time.Time) ([]AdapterStatus, error) {
	adapterCacheMu.Lock()
	defer adapterCacheMu.Unlock()
	if adapterCache != nil && now.Sub(adapterCacheAt) < adapterCacheTTL {
		return ApplyDisabled(adapterCache), nil
	}
	adapters, err := adapterProbe()
	if err != nil {
		return nil, err
	}
	adapterCache = adapters
	adapterCacheAt = now
	return ApplyDisabled(adapters), nil
}
