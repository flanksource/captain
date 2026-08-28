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

// ParseRuntimeMode validates the authored backend token used by the compact
// model grammar and the Model backend field. The accepted values are deliberately
// exact: old provider names and composite adapter ids are invalid configuration.
func ParseRuntimeMode(s string) (RuntimeMode, bool) {
	switch RuntimeMode(strings.ToLower(strings.TrimSpace(s))) {
	case ModeAPI:
		return ModeAPI, true
	case ModeCLI:
		return ModeCLI, true
	case ModeAgent:
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
