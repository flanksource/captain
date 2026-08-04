package ai

import "sync"

// RuntimeStatus describes provider-owned local prerequisites independently of
// authentication. A provisioner may make a runtime ready on first use.
type RuntimeStatus struct {
	Binary            string
	BinaryMissing     string
	DependencyMissing string
	Provisioner       string
	Error             string
}

type RuntimeProbe func() RuntimeStatus

var (
	runtimeProbeMu sync.RWMutex
	runtimeProbes  = map[Backend]RuntimeProbe{}
)

// RegisterRuntimeProbe lets a provider report the prerequisites its real
// launch path uses instead of relying on a generic PATH check.
func RegisterRuntimeProbe(backend Backend, probe RuntimeProbe) {
	runtimeProbeMu.Lock()
	defer runtimeProbeMu.Unlock()
	runtimeProbes[backend] = probe
}

func probeRuntime(backend Backend) (RuntimeStatus, bool) {
	runtimeProbeMu.RLock()
	probe, ok := runtimeProbes[backend]
	runtimeProbeMu.RUnlock()
	if !ok {
		return RuntimeStatus{}, false
	}
	return probe(), true
}
