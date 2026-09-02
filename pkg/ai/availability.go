package ai

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// AvailabilityForAdapter projects provider readiness into presentation-safe
// status without exposing credentials or authentication details.
func AvailabilityForAdapter(status AdapterStatus) api.Availability {
	runtime := Runtime{Provider: status.Provider, Mode: api.RuntimeMode(status.Mode)}
	label := runtimeLabel(runtime)
	switch {
	case status.Disabled:
		return api.Availability{State: api.AvailabilityDisabled,
			Reason:      "Disabled by " + status.DisabledReason + " in Captain configuration.",
			Remediation: "Enable " + status.DisabledReason + " on the Whoami page, then refresh."}
	case status.RuntimeError != "":
		return api.Availability{State: api.AvailabilityUnavailable,
			Reason:      fmt.Sprintf("%s prerequisites could not be inspected.", label),
			Remediation: "Review the runtime diagnostics on the Whoami page, then refresh."}
	case status.DependencyMissing != "":
		return api.Availability{State: api.AvailabilityMissingDependency,
			Reason:      fmt.Sprintf("%s requires %s, which is not available.", label, quoted(status.DependencyMissing)),
			Remediation: fmt.Sprintf("Install %s or add it to PATH, then refresh.", quoted(status.DependencyMissing))}
	case status.Type == "cli" && status.Binary == "":
		binary := status.BinaryMissing
		if binary == "" {
			p, _ := api.ProviderByName(runtime.Provider)
			binary = requiredBinary(p, runtime.Mode)
		}
		return api.Availability{State: api.AvailabilityMissingExecutable,
			Reason:      fmt.Sprintf("%s was not found on PATH.", quoted(binary)),
			Remediation: fmt.Sprintf("Install %s or add it to PATH, then refresh.", label)}
	case !status.Authenticated && status.Type == "cli":
		return api.Availability{State: api.AvailabilityNotAuthenticated,
			Reason:      label + " is installed but not authenticated.",
			Remediation: "Authenticate with " + loginCommand(runtime) + ", then refresh."}
	case !status.Authenticated:
		return api.Availability{State: api.AvailabilityMissingCredential,
			Reason:      "No " + label + " credentials are configured.",
			Remediation: "Configure credentials on the Whoami page, then refresh."}
	default:
		return api.Available()
	}
}

// LiveRuntimeCatalog annotates the registry runtime catalog with host readiness.
func LiveRuntimeCatalog() ([]api.RuntimeFamily, error) {
	adapters, err := CachedAdapters(time.Now())
	if err != nil && !errors.Is(err, ErrAdapterProbeUnsettled) {
		return nil, err
	}
	return RuntimeCatalogFromAdapters(adapters), nil
}

// RuntimeCatalogFromAdapters annotates the registry catalog from an existing
// whoami probe so model and runtime pickers observe one readiness snapshot.
func RuntimeCatalogFromAdapters(adapters []AdapterStatus) []api.RuntimeFamily {
	byRuntime := make(map[Runtime]AdapterStatus, len(adapters))
	for _, adapter := range adapters {
		byRuntime[Runtime{Provider: adapter.Provider, Mode: api.RuntimeMode(adapter.Mode)}] = adapter
	}
	runtimes := api.RuntimeCatalog()
	for familyIndex := range runtimes {
		for modeIndex := range runtimes[familyIndex].Modes {
			mode := &runtimes[familyIndex].Modes[modeIndex]
			if mode.Disabled {
				continue
			}
			// Both sides key on the same (provider, mode) pair. They used to key
			// on different vocabularies — adapter statuses on the resolved adapter
			// id, the catalog entry on the authored mode — so nothing ever
			// matched and every runtime reported "readiness was not reported".
			runtime := Runtime{Provider: runtimes[familyIndex].Provider, Mode: api.RuntimeMode(mode.Mode)}
			adapter, ok := byRuntime[runtime]
			if !ok {
				mode.Availability = api.Availability{State: api.AvailabilityUnavailable,
					Reason:      runtimeLabel(runtime) + " readiness was not reported.",
					Remediation: "Review the runtime diagnostics on the Whoami page, then refresh."}
				continue
			}
			mode.Availability = AvailabilityForAdapter(adapter)
		}
	}
	return runtimes
}

func runtimeLabel(runtime Runtime) string {
	family := ""
	if p, ok := api.ProviderByName(runtime.Provider); ok {
		family = p.AgentName
	}
	if family != "" {
		family = strings.ToUpper(family[:1]) + family[1:]
	}
	switch runtime.Mode {
	case api.ModeAPI:
		return family + " API"
	case api.ModeCLI:
		return family + " CLI"
	case api.ModeAgent:
		return family + " Agent"
	case api.ModeCmux:
		return family + " cmux"
	default:
		return runtime.String()
	}
}

func loginCommand(runtime Runtime) string {
	if adapter, ok := cliAdapters()[runtime.Provider]; ok && len(adapter.logins) > 0 {
		return quoted(adapter.logins[0].label)
	}
	return quoted(runtime.Provider + " login")
}

func quoted(value string) string { return "`" + value + "`" }
