package ai

import (
	"github.com/flanksource/captain/pkg/api"
)

// Backend is an alias for the canonical api.Backend; the enum/value type and its
// helpers live in pkg/api (the leaf package) so they are the single source of
// truth and pkg/api can carry them without importing pkg/ai. The methods
// (Valid, Kind) and constants are re-exported below so existing call sites and
// clicky/aichat's captainai.Backend keep compiling unchanged.
type Backend = api.Backend

const (
	BackendAnthropic   = api.BackendAnthropic
	BackendGemini      = api.BackendGemini
	BackendOpenAI      = api.BackendOpenAI
	BackendDeepSeek    = api.BackendDeepSeek
	BackendClaudeCLI   = api.BackendClaudeCLI
	BackendCodexCLI    = api.BackendCodexCLI
	BackendGeminiCLI   = api.BackendGeminiCLI
	BackendClaudeAgent = api.BackendClaudeAgent
	BackendCodexAgent  = api.BackendCodexAgent
	BackendClaudeCmux  = api.BackendClaudeCmux
	BackendCodexCmux   = api.BackendCodexCmux
)

// AllBackends lists every supported backend in canonical order.
func AllBackends() []Backend { return api.AllBackends() }

// AuthEnvVars returns the environment variables consulted for a backend's API key.
func AuthEnvVars(b Backend) []string { return api.AuthEnvVars(b) }

// BackendList renders AllBackends as a comma-separated string for help/error text.
func BackendList() string { return api.BackendList() }

// Provider and StreamingProvider are the buffered/streaming execution interfaces.
// They live in pkg/api (the stable runtime contract) and are re-exported here so
// existing call sites keep compiling unchanged.
type Provider = api.Provider
type StreamingProvider = api.StreamingProvider

// InferBackend resolves the backend from a model name prefix (delegates to pkg/api).
func InferBackend(model string) (Backend, error) { return api.InferBackend(model) }
