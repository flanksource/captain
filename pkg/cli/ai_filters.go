package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	capapi "github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/runtimeprofiles"
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
		aiEnumFilter[T]{key: "fallback", label: "Fallback Models", options: modelFilterOptions},
		aiEnumFilter[T]{key: "multi-models", label: "Models", options: modelFilterOptions},
		aiEnumFilter[T]{key: "effort", label: "Reasoning Effort", options: effortFilterOptions},
		aiEnumFilter[T]{key: "scope", label: "Verifier Scope", options: scopeFilterOptions},
		aiEnumFilter[T]{key: "mode", label: "Runtime Mode", options: modeFilterOptions},
		aiEnumFilter[T]{key: "provider", label: "Provider", options: providerFilterOptions},
		aiEnumFilter[T]{key: "permission-mode", label: "Permission Mode", options: permissionModeFilterOptions},
		aiEnumFilter[T]{key: "access", label: "Access", options: accessFilterOptions},
		aiCatalogFilter[T]{key: "sandbox", label: "Sandbox", options: sandboxFilterOptions},
		aiCatalogFilter[T]{key: "preset", label: "Runtime Presets", options: presetFilterOptions},
		aiCatalogFilter[T]{key: "perms", label: "Permission Sets", options: presetFilterOptions},
	}
}

// aiCatalogFilter is aiEnumFilter for an option set read from captain's
// config or database: it is an entity.FallibleFilter, so a source that fails
// fails the lookup and completion rather than offering nothing.
type aiCatalogFilter[T any] struct {
	key     string
	label   string
	options func(context.Context) (map[string]api.Textable, error)
}

func (f aiCatalogFilter[T]) Key() string                                { return f.key }
func (f aiCatalogFilter[T]) Label() string                              { return f.label }
func (f aiCatalogFilter[T]) Lookup(*T) (map[string]api.Textable, error) { return nil, nil }
func (f aiCatalogFilter[T]) LookupOptions(ctx context.Context, _ T) (map[string]api.Textable, error) {
	return f.options(ctx)
}

// Options exists for the entity.Filter contract only: clicky asks a
// FallibleFilter through LookupOptions, which can report the failure.
func (f aiCatalogFilter[T]) Options(T) map[string]api.Textable {
	panic(fmt.Sprintf("aiCatalogFilter %q: options are read through LookupOptions", f.key))
}

// firstClause is a description up to its first sentence or clause break:
// enough to tell options apart in a one-line label.
func firstClause(description string) string {
	end := len(description)
	for _, sep := range []string{". ", ": ", "; "} {
		if i := strings.Index(description, sep); i >= 0 && i < end {
			end = i
		}
	}
	return strings.TrimSuffix(strings.TrimSpace(description[:end]), ".")
}

func permissionModeFilterOptions() map[string]api.Textable {
	out := map[string]api.Textable{}
	for _, mode := range capapi.AllPermissionModes() {
		out[string(mode)] = api.Text{Content: string(mode)}
	}
	return out
}

func accessFilterOptions() map[string]api.Textable {
	labels := map[capapi.AccessLevel]string{capapi.AccessReadOnly: "Read-only", capapi.AccessFull: "Full"}
	out := map[string]api.Textable{}
	for _, level := range capapi.AllAccessLevels() {
		out[string(level)] = api.Text{Content: labels[level]}
	}
	return out
}

// sandboxFilterOptions offers the sandbox modes and the Docker/Git Agent
// backends configured by name, as --sandbox accepts both.
func sandboxFilterOptions(context.Context) (map[string]api.Textable, error) {
	out := map[string]api.Textable{}
	for _, mode := range capapi.AllSandboxModes() {
		out[string(mode)] = api.Text{Content: string(mode)}
	}
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return nil, fmt.Errorf("load sandbox backends: %w", err)
	}
	for name, backend := range cfg.Sandbox.Backends {
		out[name] = api.Text{Content: fmt.Sprintf("%s (%s backend)", name, backend.Kind)}
	}
	return out, nil
}

// presetFilterOptions offers the runtime presets by name (what --preset and
// --perms accept), labelled with the first clause of their descriptions.
func presetFilterOptions(ctx context.Context) (map[string]api.Textable, error) {
	catalog, err := buildRuntimeCatalog(ctx, runtimeprofiles.DefaultCatalogOptions{})
	if err != nil {
		return nil, err
	}
	presets, err := catalog.ListPresets(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]api.Textable, len(presets))
	for _, preset := range presets {
		label := preset.Name
		if summary := firstClause(preset.Description); summary != "" {
			label += " — " + summary
		}
		out[preset.Name] = api.Text{Content: label}
	}
	return out, nil
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
