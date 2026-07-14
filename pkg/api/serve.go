package api

import (
	"os"
	"strings"
)

// ServeURLEnv names the environment variable captain serve exports with its
// listen address, so hook receivers and captain-launched agents find the right
// instance even off the default port.
const ServeURLEnv = "CAPTAIN_SERVER_URL"

// DefaultServeURL is captain serve's default listen address.
const DefaultServeURL = "http://localhost:9020"

// MonitorHooksEnv disables captain's session-monitoring hook injection when
// set to "off" — for fixtures and CI runs that need deterministic agent argv.
const MonitorHooksEnv = "CAPTAIN_MONITOR_HOOKS"

// ServeBaseURL resolves the running captain serve instance: $CAPTAIN_SERVER_URL
// when set (exported by serve itself), else the default port on localhost.
func ServeBaseURL() string {
	if url := strings.TrimSpace(os.Getenv(ServeURLEnv)); url != "" {
		return url
	}
	return DefaultServeURL
}

// MonitorHooksEnabled reports whether captain injects its session-monitoring
// hooks into an agent it launches. The hooks are captain infrastructure, not
// user hooks — Memory.SkipHooks does not suppress them. Bare runs and
// CAPTAIN_MONITOR_HOOKS=off opt out.
func MonitorHooksEnabled(spec Spec) bool {
	if os.Getenv(MonitorHooksEnv) == "off" {
		return false
	}
	return !spec.Memory.Bare && !spec.Permissions.HasPreset(PresetBare)
}
