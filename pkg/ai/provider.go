package ai

import (
	"github.com/flanksource/captain/pkg/api"
)

// Runtime is an alias for the canonical api.Runtime — the (provider, mode) pair
// that decides which adapter serves a request. The type and its helpers live in
// pkg/api (over the leaf registry) so they are the single source of truth, and
// are re-exported here so call sites keep one import.
type Runtime = api.Runtime

// ModelProvider is the provider descriptor: the family that owns a model name.
type ModelProvider = api.ModelProvider

// RuntimeMode is the mechanism half of a runtime: api | agent | cli | cmux.
type RuntimeMode = api.RuntimeMode

var (
	Anthropic = api.Anthropic
	OpenAI    = api.OpenAI
	Google    = api.Google
	DeepSeek  = api.DeepSeek
)

const (
	ModeAPI   = api.ModeAPI
	ModeCLI   = api.ModeCLI
	ModeAgent = api.ModeAgent
	ModeCmux  = api.ModeCmux
)

// AllRuntimes lists every supported provider×mode pair in canonical order.
func AllRuntimes() []Runtime { return api.AllRuntimes() }

// RuntimeOf pairs a provider descriptor with a mode.
func RuntimeOf(p *ModelProvider, mode RuntimeMode) Runtime { return api.RuntimeOf(p, mode) }

// AuthEnvVars returns the environment variables consulted for a runtime API key.
func AuthEnvVars(p *ModelProvider, mode RuntimeMode) []string { return api.AuthEnvVars(p, mode) }

// RuntimeList renders the supported pairs, grouped by provider, for help text.
func RuntimeList() string { return api.RuntimeList() }

// Providers lists every provider descriptor in claim order.
func Providers() []*ModelProvider { return api.Providers() }

// ProviderList renders the provider keys for help and error text.
func ProviderList() string { return api.ProviderList() }

// Provider and StreamingProvider are the buffered/streaming execution interfaces.
// They live in pkg/api (the stable runtime contract) and are re-exported here so
// existing call sites keep compiling unchanged.
type Provider = api.Provider
type StreamingProvider = api.StreamingProvider

// ProviderFor resolves the family that owns a model name (delegates to pkg/api).
func ProviderFor(model string) (*ModelProvider, error) { return api.ProviderFor(model) }

// ProviderByName resolves a provider descriptor by name, catalog prefix, or
// pricing prefix.
func ProviderByName(name string) (*ModelProvider, bool) { return api.ProviderByName(name) }
