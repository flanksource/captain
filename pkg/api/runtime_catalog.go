package api

import "github.com/flanksource/captain/pkg/api/registry"

// RuntimeFamily is one provider projected for a picker: which modes it serves,
// which backend each mode serializes to, and whether the user has switched any
// of them off.
//
// This is the served answer to "what can I pick?". Before it existed, every
// surface re-derived that from a hardcoded list — clicky-ui's SPEC_RUNTIME_FAMILIES,
// configure's backendOptions, the webapp's model-prefix ladders, xero-cli's
// aiProviderOrder — and they disagreed: cmux was unreachable in two of them and
// DeepSeek in a third.
//
// It carries ids, not labels. claude|codex|gemini|deepseek and api|cli|agent|cmux
// are enough for a client to render its own text and icons, and a label field
// here would be a second place for presentation to drift.
type RuntimeFamily struct {
	// Family is the coding-agent name users speak in: claude, codex, gemini,
	// deepseek. Provider is the canonical key (google, not gemini).
	Family string `json:"family"`
	// Provider is the canonical provider key: anthropic | openai | google | deepseek.
	Provider string `json:"provider"`
	// CatalogPrefix namespaces this provider's API model ids. Google's is
	// "googleai" and is deliberately not its Provider or PricingPrefix; serving
	// it here means clients never have to encode that rename themselves.
	CatalogPrefix string `json:"catalogPrefix"`
	// Modes are this provider's runtime modes in canonical order.
	Modes []RuntimeModeEntry `json:"modes"`
}

// RuntimeModeEntry is one provider×mode cell — the same pair
// registry.ModeCapabilities describes, reduced to what a picker needs.
type RuntimeModeEntry struct {
	// Backend is the portable authored value: api | agent | cli | cmux. Family
	// carries provider identity, so clients never need a composite adapter id.
	Backend string `json:"backend"`
	// Adapter is Captain's resolved implementation id for internal callers. It is
	// deliberately absent from the wire contract.
	Adapter string `json:"-"`
	// Kind is "api" or "cli": whether the mode runs in-process against a remote
	// API or supervises a local binary.
	Kind string `json:"kind"`
	// Keyless marks modes that ride the local CLI's own login and never consult
	// an API key.
	Keyless bool `json:"keyless"`
	// DefaultModel is the id a picker should seed this mode with — the
	// provider's current top pick, already skipping disabled models. Serving it
	// is what lets a client stop shipping its own "claude-sonnet-5" literal,
	// which went stale on every model release.
	DefaultModel string `json:"defaultModel,omitempty"`
	// CatalogProvider is the provider key used by /api/chat/models for this
	// mode. Local Claude/Codex modes share their agent catalog. It is always
	// served: a client joining the model list to a mode falls back to the
	// family's CatalogPrefix when it is absent, which puts every local Claude
	// mode on "anthropic" and hands the Agent picker the Anthropic API rows.
	CatalogProvider string `json:"catalogProvider"`
	// Disabled reports the user's opt-out. The entry is still served so a UI can
	// explain the absence instead of silently shrinking the picker.
	Disabled bool `json:"disabled"`
	// DisabledReason names the switch that turned it off — "mode cmux",
	// "provider deepseek", "backend claude-agent" — or "" when enabled.
	DisabledReason string       `json:"disabledReason,omitempty"`
	Availability   Availability `json:"availability"`
	// Permissions is the declared permission surface for this backend: which
	// postures it honours, which per-tool policies it can enforce and from which
	// source, and which resources it can switch. It is served here, on the static
	// catalog, rather than on the probe result, because it is a property of the
	// adapter rather than of the machine — an unprobed backend still has an
	// honest answer, and a client that reads it from a TTL'd cache would render
	// an empty tree as "this backend supports nothing".
	Permissions PermissionCapabilities `json:"permissions"`
	// Schema is the supported Spec surface for this mode. Native CLI and agent
	// protocol bindings are attached to their owning fields with x-clicky
	// annotations, so editors and provider mappings read one contract.
	Schema map[string]any `json:"schema"`
}

// RuntimeCatalog projects the provider registry into the picker descriptor,
// annotated with the installed opt-out set.
//
// Annotated, not filtered: whoami and runtime pickers render disabled entries
// with an explanation. Filtering here would make the missing choice opaque.
func RuntimeCatalog() []RuntimeFamily {
	disabled := Disabled()
	out := make([]RuntimeFamily, 0, len(registry.Providers()))
	for _, p := range registry.Providers() {
		family := RuntimeFamily{
			Family:        p.AgentName,
			Provider:      p.Name,
			CatalogPrefix: p.CatalogPrefix,
			Modes:         make([]RuntimeModeEntry, 0, len(p.Modes())),
		}
		for _, mode := range p.Modes() {
			caps, ok := p.Caps(mode)
			if !ok {
				continue
			}
			reason := disabled.Reason(caps.Backend)
			availability := Available()
			if reason != "" {
				availability = Availability{
					State:       AvailabilityDisabled,
					Reason:      "Disabled by " + reason + " in Captain configuration.",
					Remediation: "Enable " + reason + " on the Whoami page, then refresh.",
				}
			}
			family.Modes = append(family.Modes, RuntimeModeEntry{
				Backend:         string(mode),
				Adapter:         string(caps.Backend),
				Kind:            caps.Backend.Kind(),
				Keyless:         caps.Keyless,
				DefaultModel:    DefaultModelFor(caps.Backend),
				CatalogProvider: CatalogProviderFor(caps.Backend),
				Disabled:        disabled.Backend(caps.Backend),
				DisabledReason:  reason,
				Availability:    availability,
				Permissions:     PermissionCapabilitiesFor(caps.Backend),
				Schema:          RuntimeSchemaFor(caps.Backend),
			})
		}
		out = append(out, family)
	}
	return out
}

// DefaultModelFor is the model a picker should seed for one backend.
func DefaultModelFor(b Backend) string { return registry.DefaultModelFor(b) }

// CatalogProviderFor is the model provider axis served to runtime pickers.
// Runtime mode is carried independently by RuntimeModeEntry.Backend, so this
// value never contains an adapter or execution mechanism.
func CatalogProviderFor(b Backend) string {
	return CatalogPrefixFor(b)
}

// CatalogPrefixFor is the namespace a backend's model ids live under:
// "anthropic" for every Claude mode, "googleai" — not "google" — for Gemini.
//
// It is the grouping key a model menu buckets on, and the same string
// RuntimeFamily.CatalogPrefix serves, so a client filtering a flat model list
// down to one family joins the two on a value neither side had to spell out.
// It returns "" for an unknown backend.
func CatalogPrefixFor(b Backend) string {
	p, _, ok := registry.ProviderFor(b)
	if !ok {
		return ""
	}
	return p.CatalogPrefix
}
