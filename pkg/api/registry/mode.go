package registry

import "strings"

// RuntimeMode is the mechanism that serves a model: called directly over HTTP,
// driven through an installed CLI binary, run via an agent SDK subprocess, or
// piloted in an interactive TUI inside a cmux surface.
//
// It is the only half of a runtime a caller authors — the provider half is
// derived from the model name. Captain used to compress the pair into a
// composite id ("claude-agent" for anthropic×agent) and serialize that instead,
// which meant the same JSON key named an adapter outbound and a mode inbound.
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

// Kind classifies a mode as "api" (called directly over HTTP with an API key)
// or "cli" (delegated to an installed coding-agent binary with its own auth).
// It is a property of the mechanism alone — no provider input.
func (m RuntimeMode) Kind() string {
	if m == ModeAPI {
		return "api"
	}
	return "cli"
}

// RuntimeModeList renders the modes as comma-separated text for help/errors.
func RuntimeModeList() string {
	parts := make([]string, len(AllRuntimeModes()))
	for i, m := range AllRuntimeModes() {
		parts[i] = string(m)
	}
	return strings.Join(parts, ", ")
}
