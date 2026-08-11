package ai

import (
	"errors"
	"sync"
	"time"
)

// adapterCacheTTL bounds how long a probed adapter set is reused. A long-running
// server (whoami-backed model menu, prompt schema) refreshes on this cadence so
// key/login/model changes surface without a probe per request.
const adapterCacheTTL = 60 * time.Second

// adapterAuthProbe captures the current host identity before a cache lookup;
// adapterProbe resolves adapters from that same frozen snapshot. Both are
// package vars so tests can substitute deterministic, network-free stubs.
var adapterAuthProbe = OSAuthProbe
var adapterProbe = func(probe AuthProbe) ([]AdapterStatus, error) {
	return ProbeAdapters(WhoamiOptions{Models: true}, probe)
}

var (
	adapterCacheMu          sync.Mutex
	adapterCache            []AdapterStatus
	adapterCacheAt          time.Time
	adapterCacheFingerprint string
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
	probe := freezeAuthProbe(adapterAuthProbe())
	if probe.ProbeError != nil {
		return nil, probe.ProbeError
	}
	if adapterCache != nil && adapterCacheFingerprint == probe.stateFingerprint && now.Sub(adapterCacheAt) < adapterCacheTTL {
		return ApplyDisabled(adapterCache), nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		adapters, err := adapterProbe(probe)
		if err != nil {
			return nil, err
		}
		// Codex model discovery runs an external process whose account cannot be
		// injected. Re-capture cheap host state before publishing its result; one
		// retry closes a login/binary change that happened during the probe.
		current := freezeAuthProbe(adapterAuthProbe())
		if current.ProbeError == nil && current.stateFingerprint == probe.stateFingerprint {
			adapterCache = cloneAdapterStatuses(adapters)
			adapterCacheAt = now
			adapterCacheFingerprint = probe.stateFingerprint
			return ApplyDisabled(adapterCache), nil
		}
		if current.ProbeError != nil {
			return nil, current.ProbeError
		}
		if attempt == 1 {
			break
		}
		probe = current
	}
	return nil, errors.New("adapter probe did not settle")
}
