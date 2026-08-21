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

// ErrAdapterProbeUnsettled reports that authentication state changed throughout
// adapter discovery. The returned adapter snapshot remains usable but is not
// cached.
var ErrAdapterProbeUnsettled = errors.New("adapter probe did not settle")

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
// failure does not permanently empty the catalog. If host state never settles,
// the freshest result is returned with ErrAdapterProbeUnsettled and is not
// cached. `now` is a parameter so tests can advance time deterministically.
//
// The cache stores the raw probe; the user's opt-out set is applied on the way
// out. Baking it in would make a toggle wait out the TTL.
func CachedAdapters(now time.Time) ([]AdapterStatus, error) {
	rawProbe := adapterAuthProbe()
	if rawProbe.ProbeError != nil {
		return nil, rawProbe.ProbeError
	}
	hint := freezeAuthProbeHint(rawProbe)

	adapterCacheMu.Lock()
	if adapterCache != nil && adapterCacheFingerprint == hint.stateFingerprint && now.Sub(adapterCacheAt) < adapterCacheTTL {
		adapters := ApplyDisabled(adapterCache)
		adapterCacheMu.Unlock()
		return adapters, nil
	}
	adapterCacheMu.Unlock()

	// Full credential-file hashing is only needed after the metadata hint says
	// the cache may be stale. Do it outside the cache lock so unrelated readers
	// do not queue behind disk I/O.
	probe := freezeAuthProbe(rawProbe)

	adapterCacheMu.Lock()
	defer adapterCacheMu.Unlock()
	if adapterCache != nil && adapterCacheFingerprint == hint.stateFingerprint && now.Sub(adapterCacheAt) < adapterCacheTTL {
		return ApplyDisabled(adapterCache), nil
	}

	var latest []AdapterStatus
	for attempt := 0; attempt < 2; attempt++ {
		adapters, err := adapterProbe(probe)
		if err != nil {
			return nil, err
		}
		latest = adapters

		// Codex model discovery runs an external process whose account cannot be
		// injected. Re-capture host state before publishing its result; one retry
		// closes a login or binary change that happened during the probe.
		currentRaw := adapterAuthProbe()
		if currentRaw.ProbeError != nil {
			return nil, currentRaw.ProbeError
		}
		currentHint := freezeAuthProbeHint(currentRaw)
		current := freezeAuthProbe(currentRaw)
		if current.stateFingerprint == probe.stateFingerprint {
			adapterCache = cloneAdapterStatuses(adapters)
			adapterCacheAt = now
			adapterCacheFingerprint = currentHint.stateFingerprint
			return ApplyDisabled(adapterCache), nil
		}
		probe = current
	}
	return ApplyDisabled(latest), ErrAdapterProbeUnsettled
}

// freezeAuthProbeHint captures the same state dimensions as freezeAuthProbe but
// uses file metadata in place of file contents when the host probe supports it.
func freezeAuthProbeHint(probe AuthProbe) AuthProbe {
	if probe.FileMetadataIdentity != nil {
		probe.FileIdentity = probe.FileMetadataIdentity
	}
	return freezeAuthProbe(probe)
}
