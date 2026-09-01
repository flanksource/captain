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
	runtimeProbes  = map[Runtime]RuntimeProbe{}
)

// RegisterRuntimeProbe lets a provider report the prerequisites its real
// launch path uses instead of relying on a generic PATH check.
func RegisterRuntimeProbe(p *ModelProvider, mode RuntimeMode, probe RuntimeProbe) {
	runtimeProbeMu.Lock()
	defer runtimeProbeMu.Unlock()
	runtimeProbes[RuntimeOf(p, mode)] = probe
}

func probeRuntime(p *ModelProvider, mode RuntimeMode) (RuntimeStatus, bool) {
	runtimeProbeMu.RLock()
	probe, ok := runtimeProbes[RuntimeOf(p, mode)]
	runtimeProbeMu.RUnlock()
	if !ok {
		return RuntimeStatus{}, false
	}
	return probe(), true
}
