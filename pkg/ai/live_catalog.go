package ai

import (
	"errors"
	"time"

	"github.com/flanksource/captain/pkg/api"
)

// LiveCatalog is the model menu reconciled with what the host actually exposes
// right now (the cached whoami probe): live provider /v1/models and the codex
// debug catalog are merged over the static registry projection, keyed by menu
// ID. Static entries are never dropped — an API model with no key stays in the
// menu (rendered disabled by LiveCatalogInfo) so the picker still communicates
// what a key would unlock — but any model the probe describes with fresher data
// overrides its static counterpart.
func LiveCatalog() ([]Model, error) {
	adapters, err := CachedAdapters(time.Now())
	if err != nil && !errors.Is(err, ErrAdapterProbeUnsettled) {
		return nil, err
	}
	return mergeLiveCatalog(Catalog(), adapters, liveCatalogOptions{}), nil
}

// LiveCatalogInfo annotates the live catalog with per-caller selectability,
// reusing the same rules as CatalogInfo: API models are configured when their
// provider key is present (configuredProviders), agent/CLI models when their
// local backend binary is installed.
func LiveCatalogInfo(configuredProviders []string) ([]ModelInfo, error) {
	adapters, err := CachedAdapters(time.Now())
	if err != nil && !errors.Is(err, ErrAdapterProbeUnsettled) {
		return nil, err
	}
	models := mergeLiveCatalog(catalogSnapshot(), adapters, liveCatalogOptions{IncludeDisabled: true})
	return catalogInfoFrom(models, catalogInfoOptions{
		ConfiguredProviders: configuredProviders,
		Adapters:            adapters,
	}), nil
}

// mergeLiveCatalog upserts each probed model onto the static catalog. Ordering
// follows the static catalog, with live-only models appended in probe order so
// the menu stays stable across refreshes.
//
// Disabled entries are skipped against both the probed runtime and the menu
// runtime it collapses onto: without the menu-runtime check, disabling a model
// on the Claude agent card would still let the Claude cli probe re-add it under
// the same menu id.
type liveCatalogOptions struct {
	IncludeDisabled bool
}

func mergeLiveCatalog(static []Model, adapters []AdapterStatus, options liveCatalogOptions) []Model {
	out := append([]Model(nil), static...)
	pos := make(map[string]int, len(out))
	for i, m := range out {
		pos[m.ID] = i
	}

	disabled := Disabled()
	for _, a := range adapters {
		probedProvider, known := api.ProviderByName(a.Provider)
		if !known {
			continue
		}
		probed := RuntimeOf(probedProvider, api.RuntimeMode(a.Mode))
		menu, hasMenu := menuRuntimeFor(probed)
		if !hasMenu || (!options.IncludeDisabled && disabled.Runtime(probedProvider, probed.Mode)) {
			continue
		}
		for _, md := range a.ModelDetails {
			if !options.IncludeDisabled && (disabled.Model(probedProvider, probed.Mode, md.ID) || disabled.Model(probedProvider, menu.Mode, md.ID)) {
				continue
			}
			live := liveModel(menu, md)
			if idx, seen := pos[live.ID]; seen {
				out[idx] = mergeModel(out[idx], live)
				continue
			}
			pos[live.ID] = len(out)
			out = append(out, live)
		}
	}
	return out
}

// menuRuntimeFor maps a probed runtime onto the one the menu shows for that
// model, and whether it has a menu representation at all. Every local transport
// of a family collapses onto its agent row — the menu offers one local entry per
// model, because all three drive the same binary's model list. Google has no
// local row (its CLI models already appear under the googleai API entry), so it
// returns false and is skipped. Whether an id is provider-prefixed is decided
// later by the menu mode's Kind (see liveModel).
func menuRuntimeFor(runtime Runtime) (Runtime, bool) {
	p, ok := api.ProviderByName(runtime.Provider)
	if !ok {
		return Runtime{}, false
	}
	if runtime.Mode.Kind() == "api" {
		return runtime, true
	}
	if _, serves := p.Caps(ModeAgent); !serves {
		return Runtime{}, false
	}
	return RuntimeOf(p, ModeAgent), true
}

// liveModel builds a catalog Model from one probed model detail, in the menu's
// id convention (provider-prefixed for the API mode, exact id for the local
// transports). ContextWindow/AdaptiveThinking are not carried by the live probe;
// a model already in the static catalog keeps them via mergeModel, and a
// live-only model leaves ContextWindow zero (the usage gauge degrades to no
// denominator rather than a fabricated one).
func liveModel(menu Runtime, md ModelDef) Model {
	provider, _ := api.ProviderByName(menu.Provider)
	bare := bareProviderModelID(md.ID)
	id := bare
	if menu.Mode.Kind() == "api" {
		id = CatalogPrefixOf(provider) + "/" + bare
	}
	label := md.Name
	if label == "" {
		label = bare
	}
	supported, defaultEffort := enabledEfforts(md.SupportedEfforts, md.DefaultEffort)
	return Model{
		ID:               id,
		Provider:         provider,
		Mode:             menu.Mode,
		Label:            label,
		Reasoning:        md.Reasoning,
		Temperature:      md.Temperature,
		ReleaseDate:      md.ReleaseDate,
		SupportedEfforts: supported,
		DefaultEffort:    defaultEffort,
		Priority:         md.Priority,
	}
}

// mergeModel refreshes a static catalog entry with live probe data while keeping
// the richer static metadata (menu label, context window, adaptive-thinking
// flag) that the live probe does not carry.
func mergeModel(static, live Model) Model {
	merged := static
	if live.ReleaseDate != "" {
		merged.ReleaseDate = live.ReleaseDate
	}
	if len(live.SupportedEfforts) > 0 {
		merged.SupportedEfforts = live.SupportedEfforts
	}
	if live.DefaultEffort != api.EffortNone {
		merged.DefaultEffort = live.DefaultEffort
	}
	merged.Reasoning = live.Reasoning
	merged.Temperature = live.Temperature
	return merged
}
