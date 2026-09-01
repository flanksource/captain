package api

import "github.com/flanksource/captain/pkg/api/registry"

// Model identity lives in pkg/api/registry — a leaf package, because decoding a
// Spec parses model strings and the parser therefore cannot sit above pkg/api.
// These aliases keep api.Model / api.Runtime / api.Effort the names the rest of
// captain uses: an alias is the identical type, methods included, so nothing
// downstream (including pkg/ai's own re-exports) has to know registry exists.

type (
	DisabledSet      = registry.DisabledSet
	Effort           = registry.Effort
	Model            = registry.Model
	ModelList        = registry.ModelList
	ModeCapabilities = registry.ModeCapabilities
	ModelProvider    = registry.Provider
	Runtime          = registry.Runtime
	RuntimeMode      = registry.RuntimeMode
	SchemaDialect    = registry.SchemaDialect
)

var (
	// Anthropic, OpenAI, Google and DeepSeek are the provider descriptors. A
	// provider is derived from a model name, never selected alongside one.
	Anthropic = registry.Anthropic
	OpenAI    = registry.OpenAI
	Google    = registry.Google
	DeepSeek  = registry.DeepSeek
)

const (
	SchemaDialectNone      = registry.SchemaDialectNone
	SchemaDialectAnthropic = registry.SchemaDialectAnthropic
	SchemaDialectOpenAI    = registry.SchemaDialectOpenAI

	ModeAPI   = registry.ModeAPI
	ModeCLI   = registry.ModeCLI
	ModeAgent = registry.ModeAgent
	ModeCmux  = registry.ModeCmux

	EffortNone   = registry.EffortNone
	EffortLow    = registry.EffortLow
	EffortMedium = registry.EffortMedium
	EffortHigh   = registry.EffortHigh
	EffortXHigh  = registry.EffortXHigh
	EffortMax    = registry.EffortMax
	EffortUltra  = registry.EffortUltra

	CodexAutoReviewModel = registry.CodexAutoReviewModel

	// DefaultModelID is captain's declared default model.
	DefaultModelID = registry.DefaultModelID
)

// ErrUnknownModel marks the "no provider claims this model name" failure so
// callers can enrich it (e.g. with "did you mean" model suggestions).
var ErrUnknownModel = registry.ErrUnknownModel

// AllRuntimes lists every supported provider×mode pair in canonical order.
func AllRuntimes() []Runtime { return registry.AllRuntimes() }

// RuntimeOf pairs a provider descriptor with a mode.
func RuntimeOf(p *ModelProvider, mode RuntimeMode) Runtime { return registry.RuntimeOf(p, mode) }

// RuntimeList renders the supported pairs, grouped by provider, for help text.
func RuntimeList() string { return registry.RuntimeList() }

// ProviderList renders the provider keys for help and error text.
func ProviderList() string { return registry.ProviderList() }

// ProviderByName resolves a provider by Name, CatalogPrefix, or PricingPrefix.
func ProviderByName(name string) (*ModelProvider, bool) { return registry.ProviderByName(name) }

// Providers returns the provider families in canonical claim order.
func Providers() []*ModelProvider { return registry.Providers() }

// AuthEnvVars returns the environment variables consulted for a runtime's API
// key, in priority order.
func AuthEnvVars(p *ModelProvider, mode RuntimeMode) []string { return registry.AuthEnvVars(p, mode) }

// SupportsCallerTools reports whether a runtime can expose caller-supplied tools.
func SupportsCallerTools(p *ModelProvider, mode RuntimeMode) bool {
	return registry.SupportsCallerTools(p, mode)
}

// ProviderFor resolves the family that owns a model name, failing loud when the
// name matches nothing. It answers about the family only — a mode is never
// inferred from a name.
func ProviderFor(model string) (*ModelProvider, error) { return registry.ProviderFor(model) }

// ResolveModel turns an authored selection into a concrete one: the exact model
// id the driver is handed, the mode that serves it, and the provider that owns
// it. It is the single resolution point — after it, nothing derives a runtime
// again.
func ResolveModel(model Model) (Model, error) { return registry.ResolveModel(model) }

// RuntimeIdentity is the wire form of a resolved runtime selection: which model
// ran, on which mechanism. Model cannot serialize one on its own because its
// Provider is json:"-", so any response reporting a resolved runtime projects it
// through this type instead of marshalling the model directly.
//
// There is no adapter field: a runtime is (model, mode), and provider identity
// is recoverable from the model name. The composite id this type used to carry
// under `backend` meant the adapter outbound and the mode inbound, so a client
// echoing a response back as a request named a different runtime than it read.
type RuntimeIdentity struct {
	Model    string      `json:"model,omitempty"`
	Mode     RuntimeMode `json:"mode,omitempty"`
	Provider string      `json:"provider,omitempty"`
	Effort   Effort      `json:"effort,omitempty"`
}

// RuntimeIdentityOf projects a resolved model onto its wire identity.
func RuntimeIdentityOf(model Model) RuntimeIdentity {
	identity := RuntimeIdentity{Model: model.Name, Mode: model.Mode, Effort: model.Effort}
	if model.Provider != nil {
		identity.Provider = model.Provider.Name
	}
	return identity
}

// Runtime is the (provider, mode) pair this identity names.
func (r RuntimeIdentity) Runtime() Runtime {
	return Runtime{Provider: r.Provider, Mode: r.Mode}
}

// ToModel restores the identity onto a model. Only the identity fields are set;
// everything else is the caller's to supply.
func (r RuntimeIdentity) ToModel() Model {
	model := Model{Name: r.Model, Mode: r.Mode, Effort: r.Effort}
	if p, ok := registry.ProviderByName(r.Provider); ok {
		model.Provider = p
	}
	return model
}

// AllEfforts lists the non-empty effort tiers in ascending order.
func AllEfforts() []Effort { return registry.AllEfforts() }

// AllRuntimeModes lists the mechanisms that can serve a model, in wildcard
// fan-out order.
func AllRuntimeModes() []RuntimeMode { return registry.AllRuntimeModes() }

// ParseRuntimeMode normalizes a mode token, reporting whether it names a mode.
func ParseRuntimeMode(s string) (RuntimeMode, bool) { return registry.ParseRuntimeMode(s) }

// RuntimeModeList renders AllRuntimeModes as comma-separated text for errors.
func RuntimeModeList() string { return registry.RuntimeModeList() }

// NewDisabledSet builds the opt-out lookup from the raw config lists.
func NewDisabledSet(modes, providers []string, runtimes []Runtime, models, efforts []string) DisabledSet {
	return registry.NewDisabledSet(modes, providers, runtimes, models, efforts)
}

// Disabled returns the process-wide opt-out set installed from ~/.captain.yaml.
func Disabled() DisabledSet { return registry.Disabled() }

// SetDisabled installs the process-wide opt-out set.
func SetDisabled(d DisabledSet) { registry.SetDisabled(d) }
