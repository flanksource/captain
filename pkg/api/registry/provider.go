package registry

import (
	"fmt"
	"strings"
)

// SchemaDialect names the JSON-Schema subset an adapter accepts on its
// structured-output path. It is declared per cell rather than derived from the
// provider or the mode because it is neither: cmux submits no response format at
// all, so it sits outside both subsets while its API and agent siblings sit
// inside them.
type SchemaDialect string

const (
	// SchemaDialectNone marks a cell that submits no schema to a provider.
	SchemaDialectNone SchemaDialect = ""
	// SchemaDialectAnthropic is Anthropic's tool-input-schema subset.
	SchemaDialectAnthropic SchemaDialect = "anthropic"
	// SchemaDialectOpenAI is OpenAI's structured-output subset.
	SchemaDialectOpenAI SchemaDialect = "openai"
)

// ModeCapabilities is one provider×mode cell: what that adapter can do, and the
// facts about it that are neither a property of the provider nor of the mode.
//
// The runtime interface assertions (StreamingProvider, InterruptibleProvider,
// SteerableProvider) stay authoritative at execution time; this table is the
// static declaration callers can consult before constructing a provider, and the
// reason capabilities can be answered without spinning one up.
type ModeCapabilities struct {
	// Streaming: the adapter can stream incremental events.
	Streaming bool
	// Resume: the adapter can resume a prior session by id.
	Resume bool
	// Interrupt: the adapter implements InterruptibleProvider.
	Interrupt bool
	// Steer: the adapter implements SteerableProvider.
	Steer bool
	// CallerTools reports that the adapter can expose caller-supplied
	// api.Config.Tools rather than only its built-in tool ecosystem.
	CallerTools bool
	// ToolPolicy reports that the adapter can carry Permissions.Tools — the
	// per-tool allow/deny policy — to the agent. It is declared rather than
	// inferred because a backend that cannot carry a deny-list must refuse the
	// run: silently dropping the one field whose whole purpose is to forbid a
	// tool is the failure this flag exists to prevent.
	ToolPolicy bool
	// MediaTypes is the adapter's attachment ceiling. A model's own declared
	// types are clamped against it — the adapter cannot carry what it cannot send.
	MediaTypes []string
	// Keyless marks modes that never consult EnvVars because they ride the local
	// CLI's own login (cmux).
	Keyless bool
	// RequiredBinary is the executable this cell needs on PATH, or "" when it
	// calls a remote API. It is usually the family's CLI name, and deliberately
	// is not derived from it: the Anthropic agent SDK is a vendored Node package
	// driven through "tsx", not the claude binary.
	RequiredBinary string
	// SchemaDialect is the JSON-Schema subset this cell accepts, or
	// SchemaDialectNone when it submits no response format.
	SchemaDialect SchemaDialect
	// RunsThroughClaudeCode marks cells whose request is ultimately served by the
	// Claude Code process rather than by an Anthropic API call. The agent cell
	// belongs here despite its name: its bridge sets the Agent SDK's outputFormat
	// and the SDK spawns Claude Code. Classifying it as an API consumer made every
	// structured-output agent run fail, which is why this is declared, not derived.
	RunsThroughClaudeCode bool
}

// Provider describes one model family — its auth env vars, catalog and pricing
// namespaces, per-mode capabilities, and the model rows it owns.
//
// It is a struct rather than an interface on purpose: there are exactly four
// instances and they are entirely data. An interface would invite divergent
// implementations, which is precisely the failure this type exists to end — this
// knowledge used to live in a dozen per-adapter switches (InferBackend,
// backendForMode, selectorBackend, splitModelProvider, selectorModelFamily,
// AuthEnvVars, AgentsForProvider, PricingIDs, orPrefix, pricingModelID,
// adapterInputMediaTypes) which disagreed with each other.
type Provider struct {
	// Name is the canonical provider key: anthropic | openai | google | deepseek.
	Name string
	// AgentName is the coding-agent sentinel: a bare "claude"/"codex" on a
	// non-API mode means "this provider's current model".
	AgentName string
	// CatalogPrefix namespaces catalog/menu/genkit ids. Google's is "googleai".
	CatalogPrefix string
	// PricingPrefix namespaces OpenRouter-style pricing keys. Google's is
	// "google" — deliberately NOT CatalogPrefix. Deriving one from the other is
	// how pricing lookups silently missed and reported $0.
	PricingPrefix string
	// EnvVars are the API-key environment variables in priority order.
	EnvVars []string

	// DefaultMode is the mechanism a model of this family runs on when nothing
	// selects one. It is declared per provider rather than derived from the mode
	// table's order: which mode is the sensible default is a product decision,
	// not a consequence of map iteration.
	//
	// A local agent run is captain's normal shape, so providers that serve an
	// agent mode default to it; the API mode is what a caller asks for. Google
	// and DeepSeek have no agent cell and default to api.
	DefaultMode RuntimeMode

	// modes is the per-mode capability table. A missing mode means this provider
	// does not support it (google has no agent or cmux row).
	modes map[RuntimeMode]ModeCapabilities
	// claimPrefixes are the bare model-name prefixes this provider claims.
	// Claiming decides the FAMILY and nothing else — a model name never implies
	// a mode. It used to: "claude-agent-…" and a bare "codex" forced the agent
	// and CLI modes, which was the composite adapter vocabulary smuggled inside
	// the model string, still steering the runtime after the ids themselves were
	// deleted. The mode now comes from the selector, the spec, or DefaultMode.
	claimPrefixes []string
	// identityTrim are prefixes stripped before the family split.
	identityTrim []string
	// families are the family names this provider's model ids are built from.
	families []string
	// emptyTokens are tokens that name the provider but no family; they resolve
	// to emptyFamily.
	emptyTokens []string
	emptyFamily string

	// genConfig translates effort into this provider's native request controls.
	// nil means the provider has no per-request effort knob (DeepSeek).
	genConfig func(cfg map[string]any, caps KnownModel, effort Effort, maxTokens int)
	// classifyErr refines the shared error classification for this provider's own
	// error vocabulary. nil means the shared heuristics are enough.
	classifyErr func(err error, base ErrorClass) ErrorClass
}

// SupportedEnvVars returns the auth environment variables for this provider, in
// priority order.
func (p *Provider) SupportedEnvVars() []string {
	return append([]string(nil), p.EnvVars...)
}

// Modes lists the runtime modes this provider supports, in canonical order.
func (p *Provider) Modes() []RuntimeMode {
	out := make([]RuntimeMode, 0, len(p.modes))
	for _, m := range AllRuntimeModes() {
		if _, ok := p.modes[m]; ok {
			out = append(out, m)
		}
	}
	return out
}

// Caps returns the capability row for a mode. ok is false when this provider
// does not serve that mode.
func (p *Provider) Caps(mode RuntimeMode) (ModeCapabilities, bool) {
	caps, ok := p.modes[mode]
	return caps, ok
}

// RequireMode returns the capability cell for a mode, failing loud when the
// provider does not serve it.
func (p *Provider) RequireMode(mode RuntimeMode) (ModeCapabilities, error) {
	caps, ok := p.modes[mode]
	if !ok {
		// AgentName, not Name: users speak in families ("gemini models"), not
		// provider keys ("google models").
		return ModeCapabilities{}, fmt.Errorf("mode %q is not supported for %s models (supported: %s)",
			mode, p.AgentName, modeListOf(p.Modes()))
	}
	return caps, nil
}

// Runtimes lists every runtime this provider serves, in canonical mode order.
func (p *Provider) Runtimes() []Runtime {
	out := make([]Runtime, 0, len(p.modes))
	for _, m := range p.Modes() {
		out = append(out, RuntimeOf(p, m))
	}
	return out
}

// PricingIDs returns candidate pricing keys for a model, most specific first.
func (p *Provider) PricingIDs(model string) []string {
	bare := p.bareID(model)
	return []string{p.PricingPrefix + "/" + bare, bare}
}

// DefaultModelID is captain's declared default: the model a run that names none
// receives, and the id a picker seeds itself with wherever the chosen backend
// can run it. Exactly one catalog row projects to it.
const DefaultModelID = "anthropic/claude-sonnet-5"

// DefaultModel is this provider's current top pick for a mode — the id a picker
// should seed itself with. It honours the opt-out set, so a disabled model is
// never offered as a default. ok is false when the provider does not serve the
// mode, or when every candidate is disabled.
func (p *Provider) DefaultModel(mode RuntimeMode) (string, bool) {
	m, ok := p.latestModel(mode, "")
	return m.ID, ok
}

// DefaultModelFor is the model a picker should seed for one runtime: the
// declared default wherever that runtime can run it, and the provider's current
// top pick otherwise. Both answers skip models the user disabled, so a picker
// never seeds something switched off. It returns "" for a provider that does not
// serve the mode, or when every candidate is disabled.
//
// It replaces the per-runtime literal tables that named superseded models and
// disagreed with each other about what "default" meant.
func DefaultModelFor(p *Provider, mode RuntimeMode) string {
	if p == nil {
		return ""
	}
	if _, ok := p.Caps(mode); !ok {
		return ""
	}
	if exact, ok := p.ResolveExact(mode, DefaultModelID); ok && !Disabled().Model(p, mode, exact) {
		return exact
	}
	model, _ := p.DefaultModel(mode)
	return model
}

// Models returns this provider's catalog rows.
func (p *Provider) Models() []KnownModel {
	out := make([]KnownModel, 0, len(knownModels))
	for _, m := range knownModels {
		if m.Provider == p.Name {
			out = append(out, m)
		}
	}
	return out
}

// claim reports whether this provider owns a model token. It answers about the
// family only: a name never carries a mode.
func (p *Provider) claim(token string) bool {
	t := strings.ToLower(strings.TrimSpace(token))
	for _, prefix := range p.claimPrefixes {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// bareID strips this provider's catalog namespace (and the Gemini "models/"
// namespace) from an id.
func (p *Provider) bareID(model string) string {
	model = strings.TrimSpace(model)
	for _, prefix := range []string{p.CatalogPrefix + "/", p.PricingPrefix + "/", p.Name + "/", "models/"} {
		model = strings.TrimPrefix(model, prefix)
	}
	return model
}

func modeListOf(modes []RuntimeMode) string {
	parts := make([]string, len(modes))
	for i, m := range modes {
		parts[i] = string(m)
	}
	return strings.Join(parts, ", ")
}
