package ai

import "github.com/flanksource/captain/pkg/api"

// LogIdentity renders the same compact selector notation accepted by Captain's
// model flags: mode:model[:effort]. The effort suffix is omitted when effort is
// left at the backend/model default.
//
// This lives in pkg/ai rather than pkg/ai/middleware because every layer that
// names an agent in a log line must agree on the notation the user already
// reads; middleware imports pkg/ai, so this is the lowest shared home.
func LogIdentity(backend api.Backend, model string, effort api.Effort) string {
	prefix := string(backend)
	switch backend {
	case api.BackendClaudeAgent, api.BackendCodexAgent:
		prefix = "agent"
	case api.BackendClaudeCLI, api.BackendCodexCLI, api.BackendGeminiCLI:
		prefix = "cli"
	case api.BackendClaudeCmux, api.BackendCodexCmux:
		prefix = "cmux"
	case api.BackendAnthropic, api.BackendGemini, api.BackendOpenAI, api.BackendDeepSeek:
		prefix = "api"
	}
	identity := prefix + ":" + model
	if effort != api.EffortNone {
		identity += ":" + string(effort)
	}
	return identity
}
