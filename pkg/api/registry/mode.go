package registry

import "strings"

// RuntimeMode is the mechanism that serves a model: called directly over HTTP,
// driven through an installed CLI binary, run via an agent SDK subprocess, or
// piloted in an interactive TUI inside a cmux surface.
//
// A Backend is exactly a (Provider, RuntimeMode) pair — "claude-agent" is
// anthropic×agent. Backend remains the serialized form (it is written to specs,
// session rows, and the webapp wire format); RuntimeMode is the axis captain
// reasons about, and previously existed only in the frontend's RuntimeModePicker
// while Go smeared it across Backend.Kind, isMode, isSelectorPrefix,
// backendForMode, selectorBackend, and runtimeLogIdentity.
type RuntimeMode string

const (
	ModeAPI   RuntimeMode = "api"
	ModeCLI   RuntimeMode = "cli"
	ModeAgent RuntimeMode = "agent"
	ModeCmux  RuntimeMode = "cmux"
)

// AllRuntimeModes lists the modes in canonical order: the order a wildcard
// selector ("*:opus") fans out over, most-capable mechanism first.
//
// The two hand-written lists this replaces disagreed here — AgentsForProvider
// listed the CLI before the agent, wildcardBackends the reverse — and only the
// wildcard order was user-visible. That order wins.
func AllRuntimeModes() []RuntimeMode {
	return []RuntimeMode{ModeAPI, ModeAgent, ModeCLI, ModeCmux}
}

// ParseRuntimeMode normalizes a mode token from the compact grammar. "sdk" is
// accepted as an input alias for agent but is never emitted.
func ParseRuntimeMode(s string) (RuntimeMode, bool) {
	switch RuntimeMode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeAPI:
		return ModeAPI, true
	case ModeCLI:
		return ModeCLI, true
	case ModeAgent, "sdk":
		return ModeAgent, true
	case ModeCmux:
		return ModeCmux, true
	default:
		return "", false
	}
}

// RuntimeModeList renders the modes as comma-separated text for help/errors.
func RuntimeModeList() string {
	parts := make([]string, len(AllRuntimeModes()))
	for i, m := range AllRuntimeModes() {
		parts[i] = string(m)
	}
	return strings.Join(parts, ", ")
}
