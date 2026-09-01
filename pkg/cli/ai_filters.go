package cli

import (
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	capapi "github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/api"
	"github.com/flanksource/clicky/entity"
)

// aiEnumFilter is a phantom-typed clicky entity.Filter whose option set is a
// fixed enum/catalog independent of the current flag values, so one definition
// serves every AI options struct (the opts argument is ignored). It powers shell
// completion and web/RPC typeahead for the model/effort/scope/backend flags;
// fail-loud validation of the chosen value still happens in ToConfig/ToRequest.
type aiEnumFilter[T any] struct {
	key     string
	label   string
	options func() map[string]api.Textable
}

func (f aiEnumFilter[T]) Key() string                                { return f.key }
func (f aiEnumFilter[T]) Label() string                              { return f.label }
func (f aiEnumFilter[T]) Lookup(*T) (map[string]api.Textable, error) { return nil, nil }
func (f aiEnumFilter[T]) Options(T) map[string]api.Textable          { return f.options() }

// aiFilters is the shared lookup surface for the AI commands. The completion
// binder skips keys a given command does not expose, so this single
// over-declared set is safe to return from prompt/agent/test alike (e.g. --scope
// is agent-only).
func aiFilters[T any]() []entity.Filter[T] {
	return []entity.Filter[T]{
		aiEnumFilter[T]{key: "model", label: "Model", options: modelFilterOptions},
		aiEnumFilter[T]{key: "effort", label: "Reasoning Effort", options: effortFilterOptions},
		aiEnumFilter[T]{key: "scope", label: "Verifier Scope", options: scopeFilterOptions},
		aiEnumFilter[T]{key: "mode", label: "Runtime Mode", options: modeFilterOptions},
		aiEnumFilter[T]{key: "provider", label: "Provider", options: providerFilterOptions},
	}
}

// effortFilterOptions sources the reasoning-effort values from captain's shared
// api.Effort enum — the same source ToRequest validates against — so completion
// includes the captain-owned xhigh tier that clicky's aichat.Effort lacks.
func effortFilterOptions() map[string]api.Textable {
	labels := map[capapi.Effort]string{
		capapi.EffortLow:    "Low",
		capapi.EffortMedium: "Medium",
		capapi.EffortHigh:   "High",
		capapi.EffortXHigh:  "Extra High",
		capapi.EffortMax:    "Maximum",
		capapi.EffortUltra:  "Ultra",
	}
	out := make(map[string]api.Textable, len(capapi.AllEfforts()))
	for _, e := range capapi.AllEfforts() {
		out[string(e)] = api.Text{Content: labels[e]}
	}
	return out
}

// scopeOptions sources the verifier scopes from agent.AllScopes (the same source
// ParseScope validates against).
func scopeFilterOptions() map[string]api.Textable {
	out := make(map[string]api.Textable, len(agent.AllScopes()))
	for _, s := range agent.AllScopes() {
		out[string(s)] = api.Text{Content: string(s)}
	}
	return out
}

// modeFilterOptions and providerFilterOptions source the two runtime axes from
// the registry — the same universe ParseRuntimeMode and ProviderByName validate
// against — minus the ones the user switched off. The full set stays the
// validation universe: an explicit --mode=cmux fails on the opt-out rather than
// on "unknown mode", but a picker must not offer it. Both are read here rather
// than captured, because completion runs long after config load.
func modeFilterOptions() map[string]api.Textable {
	out := make(map[string]api.Textable, len(capapi.AllRuntimeModes()))
	for _, mode := range capapi.AllRuntimeModes() {
		if capapi.Disabled().Mode(mode) {
			continue
		}
		out[string(mode)] = api.Text{Content: string(mode)}
	}
	return out
}

func providerFilterOptions() map[string]api.Textable {
	providers := capapi.Providers()
	out := make(map[string]api.Textable, len(providers))
	for _, p := range providers {
		if capapi.Disabled().Provider(p) {
			continue
		}
		out[p.Name] = api.Text{Content: p.Name}
	}
	return out
}

// modelOptions suggests the captain model catalog as bare model names that
// captain's --model accepts (ProviderFor matches bare prefixes). API ids are
// "provider/model" so the provider prefix is stripped; local-mode ids are the
// exact ids their CLI accepts and are used verbatim. A mode is never part of a
// suggestion — it is a separate axis, chosen with --mode or a compact prefix.
// Completion is suggest-only: arbitrary models are still accepted, so the catalog
// need not be exhaustive.
func modelFilterOptions() map[string]api.Textable {
	catalog := ai.Catalog()
	out := make(map[string]api.Textable, len(catalog))
	for _, m := range catalog {
		id := m.ID
		if !m.IsAgent() {
			if i := strings.IndexByte(id, '/'); i >= 0 {
				id = id[i+1:]
			}
		}
		label := m.Label
		if label == "" {
			label = id
		}
		out[id] = api.Text{Content: label}
	}
	return out
}

// Filterable implementations publish the shared lookup surface to clicky's
// AddNamedCommand, which wires shell completion + RPC typeahead automatically.
// Declared per top-level struct because a promoted method would carry the
// embedded type's T, not the command's.
func (AIPromptOptions) Filters() []entity.Filter[AIPromptOptions] {
	return aiFilters[AIPromptOptions]()
}
func (AIAgentOptions) Filters() []entity.Filter[AIAgentOptions] { return aiFilters[AIAgentOptions]() }
func (AITestOptions) Filters() []entity.Filter[AITestOptions]   { return aiFilters[AITestOptions]() }
