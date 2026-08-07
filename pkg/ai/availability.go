package ai

import (
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// AvailabilityForAdapter projects provider readiness into presentation-safe
// status without exposing credentials or authentication details.
func AvailabilityForAdapter(status AdapterStatus) api.Availability {
	backend := Backend(status.Backend)
	label := runtimeLabel(backend)
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
			binary = strings.TrimSpace(string(backend))
		}
		return api.Availability{State: api.AvailabilityMissingExecutable,
			Reason:      fmt.Sprintf("%s was not found on PATH.", quoted(binary)),
			Remediation: fmt.Sprintf("Install %s or add it to PATH, then refresh.", label)}
	case !status.Authenticated && status.Type == "cli":
		return api.Availability{State: api.AvailabilityNotAuthenticated,
			Reason:      label + " is installed but not authenticated.",
			Remediation: "Authenticate with " + loginCommand(backend) + ", then refresh."}
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
	if err != nil {
		return nil, err
	}
	byBackend := make(map[string]AdapterStatus, len(adapters))
	for _, adapter := range adapters {
		byBackend[adapter.Backend] = adapter
	}
	runtimes := api.RuntimeCatalog()
	for familyIndex := range runtimes {
		for modeIndex := range runtimes[familyIndex].Modes {
			mode := &runtimes[familyIndex].Modes[modeIndex]
			backend := Backend(mode.Backend)
			if mode.Disabled {
				continue
			}
			adapter, ok := byBackend[mode.Backend]
			if !ok {
				mode.Availability = api.Availability{State: api.AvailabilityUnavailable,
					Reason:      runtimeLabel(backend) + " readiness was not reported.",
					Remediation: "Review the runtime diagnostics on the Whoami page, then refresh."}
				continue
			}
			mode.Availability = AvailabilityForAdapter(adapter)
		}
	}
	return runtimes, nil
}

func runtimeLabel(backend Backend) string {
	family := backend.Family()
	if family != "" {
		family = strings.ToUpper(family[:1]) + family[1:]
	}
	switch backend.Mode() {
	case api.ModeAPI:
		return family + " API"
	case api.ModeCLI:
		return family + " CLI"
	case api.ModeAgent:
		return family + " Agent"
	case api.ModeCmux:
		return family + " cmux"
	default:
		return string(backend)
	}
}

func loginCommand(backend Backend) string {
	if adapter, ok := cliAdapters()[backend]; ok && len(adapter.logins) > 0 {
		return quoted(adapter.logins[0].label)
	}
	return quoted(string(backend) + " login")
}

func quoted(value string) string { return "`" + value + "`" }
