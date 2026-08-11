package registry

import (
	"fmt"
	"sort"
	"strings"
)

// ModeCapabilities is one provider×mode cell: the Backend that pair serializes
// to, plus what that adapter can actually do.
//
// The runtime interface assertions (StreamingProvider, InterruptibleProvider,
// SteerableProvider) stay authoritative at execution time; this table is the
// static declaration callers can consult before constructing a provider, and the
// reason capabilities can be answered without spinning one up.
type ModeCapabilities struct {
	// Backend is the serialized enum value for this provider×mode pair, e.g.
	// anthropic×agent → "claude-agent". These strings are frozen: they are
	// persisted in specs, session rows, and the webapp wire format.
	Backend Backend
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
}

// modeToken forces a RuntimeMode from a model-name prefix: "claude-code-…" is
// the CLI, "codex-agent-…" is the agent SDK. Matched longest-prefix-first.
type modeToken struct {
	prefix string
	mode   RuntimeMode
}

// Provider describes one model family — its auth env vars, catalog and pricing
// namespaces, per-mode capabilities, and the model rows it owns.
//
// It is a struct rather than an interface on purpose: there are exactly four
// instances and they are entirely data. An interface would invite divergent
// implementations, which is precisely the failure this type exists to end — this
// knowledge used to live in InferBackend, backendForMode, selectorBackend,
// splitModelProvider, selectorModelFamily, AuthEnvVars, AgentsForProvider,
// PricingIDs, orPrefix, pricingModelID, and adapterInputMediaTypes, which
// disagreed with each other.
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

	// modes is the per-mode capability table. A missing mode means this provider
	// does not support it (google has no agent or cmux row).
	modes map[RuntimeMode]ModeCapabilities
	// modeTokens force a mode from a model-name prefix.
	modeTokens []modeToken
	// claimPrefixes are the bare model-name prefixes this provider claims. A
	// claim with no modeToken hit lands on ModeAPI.
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

// BackendFor maps a mode onto the serialized Backend, failing loud when the
// provider does not serve it.
func (p *Provider) BackendFor(mode RuntimeMode) (Backend, error) {
	caps, ok := p.modes[mode]
	if !ok {
		// AgentName, not Name: users speak in families ("gemini models"), not
		// provider keys ("google models").
		return "", fmt.Errorf("mode %q is not supported for %s models (supported: %s)",
			mode, p.AgentName, modeListOf(p.Modes()))
	}
	return caps.Backend, nil
}

// Backends lists every Backend this provider serves, in canonical mode order.
func (p *Provider) Backends() []Backend {
	out := make([]Backend, 0, len(p.modes))
	for _, m := range p.Modes() {
		out = append(out, p.modes[m].Backend)
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

// DefaultModelFor is the model a picker should seed for one backend: the
// declared default wherever that backend can run it, and the backend provider's
// current top pick otherwise. Both answers skip models the user disabled, so a
// picker never seeds something switched off. It returns "" for an unknown
// backend, or when every candidate is disabled.
//
// It replaces the per-backend literal tables that named superseded models and
// disagreed with each other about what "default" meant.
func DefaultModelFor(b Backend) string {
	p, mode, ok := ProviderFor(b)
	if !ok {
		return ""
	}
	if exact, ok := p.ResolveExact(mode, DefaultModelID); ok && !Disabled().Model(b, exact) {
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

// claim reports whether this provider owns a model token and which mode the
// token's own prefix implies. Mode tokens are matched longest-first so
// "codex-agent-x" resolves to the agent SDK rather than the codex CLI.
func (p *Provider) claim(token string) (RuntimeMode, bool) {
	t := strings.ToLower(strings.TrimSpace(token))
	for _, mt := range p.modeTokens {
		if strings.HasPrefix(t, mt.prefix) {
			return mt.mode, true
		}
	}
	for _, prefix := range p.claimPrefixes {
		if strings.HasPrefix(t, prefix) {
			return ModeAPI, true
		}
	}
	return "", false
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

// sortModeTokens orders a provider's mode tokens longest-prefix-first so the
// most specific match wins regardless of declaration order.
func sortModeTokens(tokens []modeToken) []modeToken {
	sort.SliceStable(tokens, func(i, j int) bool {
		return len(tokens[i].prefix) > len(tokens[j].prefix)
	})
	return tokens
}
