package api

import "github.com/flanksource/captain/pkg/api/registry"

// Model identity lives in pkg/api/registry — a leaf package, because decoding a
// Spec parses model strings and the parser therefore cannot sit above pkg/api.
// These aliases keep api.Model / api.Backend / api.Effort the names the rest of
// captain uses: an alias is the identical type, methods included, so nothing
// downstream (including pkg/ai's own re-exports) has to know registry exists.

type (
	Backend   = registry.Backend
	Effort    = registry.Effort
	Model     = registry.Model
	ModelList = registry.ModelList
)

const (
	BackendAnthropic   = registry.BackendAnthropic
	BackendGemini      = registry.BackendGemini
	BackendOpenAI      = registry.BackendOpenAI
	BackendDeepSeek    = registry.BackendDeepSeek
	BackendClaudeCLI   = registry.BackendClaudeCLI
	BackendCodexCLI    = registry.BackendCodexCLI
	BackendGeminiCLI   = registry.BackendGeminiCLI
	BackendClaudeAgent = registry.BackendClaudeAgent
	BackendCodexAgent  = registry.BackendCodexAgent
	BackendClaudeCmux  = registry.BackendClaudeCmux
	BackendCodexCmux   = registry.BackendCodexCmux

	AnthropicProvider = registry.AnthropicProvider
	OpenAIProvider    = registry.OpenAIProvider
	GeminiProvider    = registry.GeminiProvider
	DeepSeekProvider  = registry.DeepSeekProvider

	EffortNone   = registry.EffortNone
	EffortLow    = registry.EffortLow
	EffortMedium = registry.EffortMedium
	EffortHigh   = registry.EffortHigh
	EffortXHigh  = registry.EffortXHigh
	EffortMax    = registry.EffortMax
	EffortUltra  = registry.EffortUltra

	CodexAutoReviewModel = registry.CodexAutoReviewModel
)

// ErrInferBackend marks the "can't infer a backend from this model name" failure
// so callers can enrich it (e.g. with "did you mean" model suggestions).
var ErrInferBackend = registry.ErrInferBackend

// AllBackends lists every supported backend in canonical order.
func AllBackends() []Backend { return registry.AllBackends() }

// BackendList renders AllBackends as a comma-separated string for help/error text.
func BackendList() string { return registry.BackendList() }

// AuthEnvVars returns the environment variables consulted for a backend's API
// key, in priority order.
func AuthEnvVars(b Backend) []string { return registry.AuthEnvVars(b) }

// InferBackend resolves the backend from a model name prefix, failing loud when
// the name matches nothing.
func InferBackend(model string) (Backend, error) { return registry.InferBackend(model) }

// AllEfforts lists the non-empty effort tiers in ascending order.
func AllEfforts() []Effort { return registry.AllEfforts() }
