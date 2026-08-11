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
// Disabled entries are skipped against both the probed backend and the menu
// backend it collapses onto: without the menu-backend check, disabling a model
// on the claude-agent card would still let the claude-cli probe re-add it under
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
		probed := Backend(a.Backend)
		menuBackend, hasMenu := menuBackendFor(probed)
		if !hasMenu || (!options.IncludeDisabled && disabled.Backend(probed)) {
			continue
		}
		for _, md := range a.ModelDetails {
			if !options.IncludeDisabled && (disabled.Model(probed, md.ID) || disabled.Model(menuBackend, md.ID)) {
				continue
			}
			live := liveModel(menuBackend, md)
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

// menuBackendFor maps a probed backend to the backend the menu uses for that
// model, and whether the backend has a menu representation at all. The three
// claude execution backends collapse onto claude-agent and the three codex ones
// onto codex-agent — the menu offers one agent entry per model. Backends with no
// menu representation (gemini-cli, whose models already appear under the googleai
// API entry) return false and are skipped. Whether an id is provider-prefixed is
// decided later by the menu backend's Kind (see liveModel).
func menuBackendFor(b Backend) (Backend, bool) {
	switch b {
	case BackendAnthropic, BackendOpenAI, BackendGemini, BackendDeepSeek:
		return b, true
	case BackendClaudeAgent, BackendClaudeCLI, BackendClaudeCmux:
		return BackendClaudeAgent, true
	case BackendCodexAgent, BackendCodexCLI, BackendCodexCmux:
		return BackendCodexAgent, true
	default:
		return "", false
	}
}

// liveModel builds a catalog Model from one probed model detail, in the menu's
// id convention (provider-prefixed for API backends, exact id for agent
// backends). ContextWindow/AdaptiveThinking are not carried by the live probe;
// a model already in the static catalog keeps them via mergeModel, and a
// live-only model leaves ContextWindow zero (the usage gauge degrades to no
// denominator rather than a fabricated one).
func liveModel(menuBackend Backend, md ModelDef) Model {
	bare := bareProviderModelID(md.ID)
	id := bare
	if menuBackend.Kind() == "api" {
		id = BackendToProvider(menuBackend) + "/" + bare
	}
	label := md.Name
	if label == "" {
		label = bare
	}
	supported, defaultEffort := enabledEfforts(md.SupportedEfforts, md.DefaultEffort)
	return Model{
		ID:               id,
		Backend:          menuBackend,
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
