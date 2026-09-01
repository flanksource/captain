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
// which disagreed with pkg/api's provider claim table about grok, sora, and
// codenames.

// ModelIdentity is captain's parsed model key.
type ModelIdentity = registry.ModelIdentity

// defaultCatalogModelID is the bare id behind DefaultModelID — the one catalog
// row that carries Default: true.
var defaultCatalogModelID = registry.StripProviderPrefix(DefaultModelID)

// ParseModelIdentity parses a model token into its provider/family/version tuple.
// The token's own provider wins; defaultProvider only decides tokens that claim
// no family of their own.
func ParseModelIdentity(defaultProvider, model string) (ModelIdentity, bool) {
	p, token, ok := registry.ProviderForToken(model)
	if !ok {
		if p, ok = registry.ProviderByName(defaultProvider); !ok {
			return ModelIdentity{}, false
		}
		token = model
	}
	return p.ParseIdentity(token)
}

func registryModelDef(m registry.KnownModel, p *ModelProvider, mode RuntimeMode) ModelDef {
	supported, defaultEffort := enabledEfforts(m.SupportedEfforts, m.DefaultEffort)
	return ModelDef{
		ID:                m.ID,
		Name:              m.Label,
		Provider:          p.Name,
		Mode:              mode,
		ReleaseDate:       m.ReleaseDate,
		CapabilitiesKnown: true,
		Reasoning:         m.Reasoning,
		Temperature:       m.Temperature,
		InputMediaTypes:   clampInputMediaTypes(p, mode, m.InputMediaTypes),
		SupportedEfforts:  supported,
		DefaultEffort:     defaultEffort,
		Priority:          m.Priority,
	}
}

// enabledEfforts drops the user-disabled tiers from a supported-effort list and
// degrades a disabled default, so every projection offers only usable efforts.
//
// Every caller applies it at read time. Baking it into a value built at package
// init would freeze the empty opt-out set that exists before config load.
func enabledEfforts(supported []api.Effort, defaultEffort api.Effort) ([]api.Effort, api.Effort) {
	disabled := Disabled()
	if disabled.Effort(defaultEffort) {
		defaultEffort = api.EffortNone
	}
	return disabled.Efforts(append([]api.Effort(nil), supported...)), defaultEffort
}

// RegistryModelDef returns the registry metadata for an exact model on a
// runtime. The boolean is false when the model is known but unavailable there.
//
// The lookup is exact (aliases aside) and deliberately does NOT resolve version
// lines: "gpt-5.6" is an API-only base model, and resolving it here would answer
// with its codex-available sibling gpt-5.6-sol and report the base as available
// on Codex. Callers that want resolution call ResolveExactModel first.
func RegistryModelDef(p *ModelProvider, mode RuntimeMode, model string) (ModelDef, bool) {
	if p == nil {
		return ModelDef{}, false
	}
	entry, found := p.Lookup(model)
	if !found {
		return ModelDef{}, false
	}
	if _, available := p.Availability(mode, entry.ID); !available {
		return ModelDef{}, false
	}
	return registryModelDef(entry, p, mode), true
}

// RegistryModelAvailability distinguishes an unknown model from a registry
// model that is intentionally unavailable on the requested runtime.
func RegistryModelAvailability(p *ModelProvider, mode RuntimeMode, model string) (known, available bool) {
	if p == nil {
		return false, false
	}
	return p.Availability(mode, model)
}

// RegistryModelDefs returns exact, provider-native model IDs for a runtime. The
// local transports are projected from their provider's model registry.
func RegistryModelDefs(p *ModelProvider, mode RuntimeMode) []ModelDef {
	if p == nil {
		return nil
	}
	disabled := Disabled()
	out := make([]ModelDef, 0)
	for _, m := range p.Models() {
		if !m.Preferred || disabled.Model(p, mode, m.ID) {
			continue
		}
		if known, available := p.Availability(mode, m.ID); !known || !available {
			continue
		}
		out = append(out, registryModelDef(m, p, mode))
	}
	SortModelsByReleaseDateDesc(out)
	return out
}

// registryCatalogModels projects the registry onto the catalog rows. It runs at
// package init, so it must not consult the opt-out set — Catalog() applies that
// at read time.
func registryCatalogModels() []Model {
	out := make([]Model, 0, len(registry.KnownModels())+5)
	for _, p := range registry.Providers() {
		if _, known := p.Caps(registry.ModeAPI); !known {
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
				Provider:         p,
				Mode:             registry.ModeAPI,
				Label:            m.Label,
				Reasoning:        m.Reasoning,
				Temperature:      m.Temperature,
				AdaptiveThinking: m.AdaptiveThinking,
				ContextWindow:    m.ContextWindow,
				ReleaseDate:      m.ReleaseDate,
				InputMediaTypes:  clampInputMediaTypes(p, registry.ModeAPI, m.InputMediaTypes),
				SupportedEfforts: m.SupportedEfforts,
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
// (not a catalog-prefixed one) and an agent-flavoured label. Like
// registryCatalogModels it runs at init and leaves filtering to Catalog().
func agentCatalogModels() []Model {
	out := make([]Model, 0)
	for _, spec := range []struct {
		provider *registry.Provider
		label    func(string) string
	}{
		{registry.Anthropic, func(l string) string { return "Claude Agent · " + strings.TrimPrefix(l, "Claude ") }},
		{registry.OpenAI, func(l string) string { return "Codex Agent · " + l }},
	} {
		if _, known := spec.provider.Caps(registry.ModeAgent); !known {
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
				Provider:         spec.provider,
				Mode:             registry.ModeAgent,
				Label:            spec.label(m.Label),
				Reasoning:        m.Reasoning,
				Temperature:      m.Temperature,
				AdaptiveThinking: m.AdaptiveThinking && spec.provider == registry.Anthropic,
				ContextWindow:    m.ContextWindow,
				ReleaseDate:      m.ReleaseDate,
				InputMediaTypes:  clampInputMediaTypes(spec.provider, registry.ModeAgent, m.InputMediaTypes),
				SupportedEfforts: m.SupportedEfforts,
				DefaultEffort:    m.DefaultEffort,
				Priority:         m.Priority,
			})
		}
	}
	return out
}

// ResolveExactModel resolves a user/catalog model token into the exact model ID
// the selected runtime should receive. It accepts old aliases for input
// compatibility but never returns an alias.
func ResolveExactModel(p *ModelProvider, mode RuntimeMode, model string) (string, bool) {
	if p == nil {
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
