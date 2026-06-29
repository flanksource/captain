package ai

import (
	"context"

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
	BackendClaudeCLI   = api.BackendClaudeCLI
	BackendCodexCLI    = api.BackendCodexCLI
	BackendGeminiCLI   = api.BackendGeminiCLI
	BackendClaudeAgent = api.BackendClaudeAgent
)

// AllBackends lists every supported backend in canonical order.
func AllBackends() []Backend { return api.AllBackends() }

// AuthEnvVars returns the environment variables consulted for a backend's API key.
func AuthEnvVars(b Backend) []string { return api.AuthEnvVars(b) }

// BackendList renders AllBackends as a comma-separated string for help/error text.
func BackendList() string { return api.BackendList() }

type Provider interface {
	Execute(ctx context.Context, req Request) (*Response, error)
	GetModel() string
	GetBackend() Backend
}

type StreamingProvider interface {
	Provider
	ExecuteStream(ctx context.Context, req Request) (<-chan Event, error)
}

// InferBackend resolves the backend from a model name prefix (delegates to pkg/api).
func InferBackend(model string) (Backend, error) { return api.InferBackend(model) }
