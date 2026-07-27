package ai

import (
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

// Model identity — claiming a name for a provider, resolving aliases and
// superseded ids, and picking the exact catalog id — lives in pkg/api/registry.
// This file is only the projection of those rows onto pkg/ai's catalog types.
//
// It used to own a second copy of that knowledge (splitModelProvider,
// ParseModelIdentity, normalizeCodexVariantAlias, isSupersededRegistryExact),
// which disagreed with pkg/api's InferBackend about grok, sora, and codenames.

// ModelIdentity is captain's parsed model key.
type ModelIdentity = registry.ModelIdentity

// defaultCatalogModelID is the bare id behind DefaultModelID — the one catalog
// row that carries Default: true.
var defaultCatalogModelID = registry.StripProviderPrefix(DefaultModelID)

// ParseModelIdentity parses a model token into its provider/family/version tuple.
// The token's own provider wins; defaultProvider only decides tokens that claim
// no family of their own.
func ParseModelIdentity(defaultProvider, model string) (ModelIdentity, bool) {
	p, token, _, ok := registry.ProviderForToken(model)
	if !ok {
		if p, ok = registry.ProviderByName(defaultProvider); !ok {
			return ModelIdentity{}, false
		}
		token = model
	}
	return p.ParseIdentity(token)
}

func registryModelDef(m registry.KnownModel, backend Backend) ModelDef {
	return ModelDef{
		ID:                m.ID,
		Name:              m.Label,
		Backend:           backend,
		ReleaseDate:       m.ReleaseDate,
		CapabilitiesKnown: true,
		Reasoning:         m.Reasoning,
		Temperature:       m.Temperature,
		InputMediaTypes:   clampInputMediaTypes(backend, m.InputMediaTypes),
		SupportedEfforts:  append([]api.Effort(nil), m.SupportedEfforts...),
		DefaultEffort:     m.DefaultEffort,
		Priority:          m.Priority,
	}
}

// RegistryModelDef returns the registry metadata for an exact model on a
// backend. The boolean is false when the model is known but unavailable there.
//
// The lookup is exact (aliases aside) and deliberately does NOT resolve version
// lines: "gpt-5.6" is an API-only base model, and resolving it here would answer
// with its codex-available sibling gpt-5.6-sol and report the base as available
// on Codex. Callers that want resolution call ResolveExactModelForBackend first.
func RegistryModelDef(backend Backend, model string) (ModelDef, bool) {
	p, mode, ok := registry.ProviderFor(backend)
	if !ok {
		return ModelDef{}, false
	}
	entry, found := p.Lookup(model)
	if !found {
		return ModelDef{}, false
	}
	if _, available := p.Availability(mode, entry.ID); !available {
		return ModelDef{}, false
	}
	return registryModelDef(entry, backend), true
}

// RegistryModelAvailability distinguishes an unknown model from a registry
// model that is intentionally unavailable on the requested backend.
func RegistryModelAvailability(backend Backend, model string) (known, available bool) {
	p, mode, ok := registry.ProviderFor(backend)
	if !ok {
		return false, false
	}
	return p.Availability(mode, model)
}

// RegistryModelDefs returns exact, provider-native model IDs for a backend. CLI
// and cmux backends are projected from their parent provider's model registry.
func RegistryModelDefs(backend Backend) []ModelDef {
	p, mode, ok := registry.ProviderFor(backend)
	if !ok {
		return nil
	}
	out := make([]ModelDef, 0)
	for _, m := range p.Models() {
		if !m.Preferred {
			continue
		}
		if known, available := p.Availability(mode, m.ID); !known || !available {
			continue
		}
		out = append(out, registryModelDef(m, backend))
	}
	SortModelsByReleaseDateDesc(out)
	return out
}

func registryCatalogModels() []Model {
	out := make([]Model, 0, len(registry.KnownModels())+5)
	for _, p := range registry.Providers() {
		apiBackend, err := p.BackendFor(registry.ModeAPI)
		if err != nil {
			continue
		}
		for _, m := range p.Models() {
			if !m.Preferred {
				continue
			}
			if known, available := p.Availability(registry.ModeAPI, m.ID); !known || !available {
				continue
			}
			out = append(out, Model{
				ID:               p.CatalogPrefix + "/" + m.ID,
				Backend:          apiBackend,
				Label:            m.Label,
				Reasoning:        m.Reasoning,
				Temperature:      m.Temperature,
				AdaptiveThinking: m.AdaptiveThinking,
				ContextWindow:    m.ContextWindow,
				ReleaseDate:      m.ReleaseDate,
				InputMediaTypes:  clampInputMediaTypes(apiBackend, m.InputMediaTypes),
				SupportedEfforts: append([]api.Effort(nil), m.SupportedEfforts...),
				DefaultEffort:    m.DefaultEffort,
				Priority:         m.Priority,
				Default:          p == registry.Anthropic && m.ID == defaultCatalogModelID,
			})
		}
	}
	out = append(out, agentCatalogModels()...)
	return out
}

// agentCatalogModels projects the agent-mode rows, which carry the bare exact id
// (not a catalog-prefixed one) and an agent-flavoured label.
func agentCatalogModels() []Model {
	out := make([]Model, 0)
	for _, spec := range []struct {
		provider *registry.Provider
		label    func(string) string
	}{
		{registry.Anthropic, func(l string) string { return "Claude Agent · " + strings.TrimPrefix(l, "Claude ") }},
		{registry.OpenAI, func(l string) string { return "Codex Agent · " + l }},
	} {
		backend, err := spec.provider.BackendFor(registry.ModeAgent)
		if err != nil {
			continue
		}
		for _, m := range spec.provider.Models() {
			if !m.Preferred {
				continue
			}
			if known, available := spec.provider.Availability(registry.ModeAgent, m.ID); !known || !available {
				continue
			}
			out = append(out, Model{
				ID:               m.ID,
				Backend:          backend,
				Label:            spec.label(m.Label),
				Reasoning:        m.Reasoning,
				Temperature:      m.Temperature,
				AdaptiveThinking: m.AdaptiveThinking && spec.provider == registry.Anthropic,
				ContextWindow:    m.ContextWindow,
				ReleaseDate:      m.ReleaseDate,
				InputMediaTypes:  clampInputMediaTypes(backend, m.InputMediaTypes),
				SupportedEfforts: append([]api.Effort(nil), m.SupportedEfforts...),
				DefaultEffort:    m.DefaultEffort,
				Priority:         m.Priority,
			})
		}
	}
	return out
}

// ResolveExactModelForBackend resolves a user/catalog model token into the exact
// model ID the selected backend should receive. It accepts old aliases for input
// compatibility but never returns an alias.
func ResolveExactModelForBackend(backend Backend, model string) (string, bool) {
	p, mode, ok := registry.ProviderFor(backend)
	if !ok {
		return registry.StripProviderPrefix(model), false
	}
	return p.ResolveExact(mode, model)
}

// ModelUsesAdaptiveThinking reports whether an Anthropic model uses adaptive
// thinking, per the registry's adaptiveThinking annotation. It accepts aliases,
// provider-prefixed, and dated model tokens by resolving them to their exact
// registry entry first.
func ModelUsesAdaptiveThinking(model string) bool {
	exact, ok := registry.Anthropic.ResolveExact(registry.ModeAPI, model)
	if !ok {
		return false
	}
	m, ok := registry.Anthropic.Lookup(exact)
	return ok && m.AdaptiveThinking
}
