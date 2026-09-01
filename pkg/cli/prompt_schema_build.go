package cli

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	clickyrpc "github.com/flanksource/clicky/rpc"
)

// buildPromptSchemaDocument is the pure assembler (no I/O), taking a probed
// adapter set and resolved sandbox defaults so tests can drive it with
// deterministic, network-free inputs.
func buildPromptSchemaDocument(adapters []AdapterStatus, sandboxes captainconfig.SandboxDefaults) (map[string]any, error) {
	adapters = enabledAdapters(adapters)
	reflected, err := reflectedSchemas()
	if err != nil {
		return nil, err
	}

	specMap, err := unmarshalMap(reflected.spec)
	if err != nil {
		return nil, fmt.Errorf("decode spec schema: %w", err)
	}
	promptMap, err := unmarshalMap(reflected.prompt)
	if err != nil {
		return nil, fmt.Errorf("decode prompt schema: %w", err)
	}
	actionMap, err := unmarshalMap(reflected.promptAction)
	if err != nil {
		return nil, fmt.Errorf("decode promptAction schema: %w", err)
	}

	if err := injectSandboxBackendEnum(specMap, sandboxes); err != nil {
		return nil, err
	}
	injectSandboxModeConditionals(specMap, sandboxes)
	runtimes, err := buildRuntimesCatalog(adapters, reflected.args)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"schemaVersion": 2,
		"source":        "captain prompt --schema",
		"spec":          specMap,
		"prompt":        promptMap,
		"promptAction":  actionMap,
		"runtimeAdapters": runtimes,
		"sandboxes":       buildSandboxCatalog(sandboxes),
		"runtimes":        enabledRuntimes(),
		"models":        PromptModelCatalog(adapters),
		"efforts":       enabledEffortNames(),
		"examples": map[string]any{
			"spec": promptSchemaExampleSpec(),
		},
	}, nil
}

// injectSandboxBackendEnum constrains both SandboxRef forms to the selectors
// the runtime can resolve: built-in kinds plus backend names from the user's
// config. Keeping the two oneOf branches identical prevents the shorthand and
// object forms from drifting apart in the workbench.
func injectSandboxBackendEnum(spec map[string]any, defaults captainconfig.SandboxDefaults) error {
	scalar, backend, err := sandboxBackendSchemaTargets(spec)
	if err != nil {
		return err
	}
	enum := toAnySlice(sandboxSelectors(defaults))
	scalar["enum"] = enum
	backend["enum"] = slices.Clone(enum)
	return nil
}

func sandboxBackendSchemaTargets(spec map[string]any) (map[string]any, map[string]any, error) {
	defs, ok := spec["$defs"].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("spec schema has no $defs")
	}
	sandboxRef, ok := defs["SandboxRef"].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("spec schema has no SandboxRef definition")
	}
	oneOf, ok := sandboxRef["oneOf"].([]any)
	if !ok || len(oneOf) != 2 {
		return nil, nil, fmt.Errorf("SandboxRef schema has %d forms, want scalar and object", len(oneOf))
	}
	scalar, ok := oneOf[0].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("SandboxRef scalar schema has unexpected type %T", oneOf[0])
	}
	object, ok := oneOf[1].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("SandboxRef object schema has unexpected type %T", oneOf[1])
	}
	properties, ok := object["properties"].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("SandboxRef object schema has no properties")
	}
	backend, ok := properties["backend"].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("SandboxRef object schema has no backend property")
	}
	return scalar, backend, nil
}

func sandboxSelectors(defaults captainconfig.SandboxDefaults) []string {
	selectors := make([]string, 0, len(api.AllSandboxes())+len(defaults.Backends))
	seen := map[string]struct{}{}
	for _, descriptor := range api.AllSandboxes() {
		selector := string(descriptor.Kind)
		selectors = append(selectors, selector)
		seen[selector] = struct{}{}
	}
	configured := make([]string, 0, len(defaults.Backends))
	for name := range defaults.Backends {
		if _, exists := seen[name]; !exists {
			configured = append(configured, name)
		}
	}
	slices.Sort(configured)
	selectors = append(selectors, configured...)
	return selectors
}

// enabledRuntimes is the provider×mode descriptor the workbench's runtime picker
// renders from. Disabled entries are dropped rather than annotated: whoami is
// the only surface that shows what is switched off.
func enabledRuntimes() []api.RuntimeFamily {
	out := make([]api.RuntimeFamily, 0, len(api.RuntimeCatalog()))
	for _, family := range api.RuntimeCatalog() {
		modes := make([]api.RuntimeModeEntry, 0, len(family.Modes))
		for _, mode := range family.Modes {
			if !mode.Disabled {
				modes = append(modes, mode)
			}
		}
		if len(modes) == 0 {
			continue
		}
		family.Modes = modes
		out = append(out, family)
	}
	return out
}

// enabledEffortNames is the effort universe a picker falls back to for a model
// the catalog does not describe. Serving it keeps the webapp from carrying its
// own copy of the tier list, which drifted from the registry and would not
// honour ai.disabled.efforts.
func enabledEffortNames() []string {
	efforts := ai.Disabled().EnabledEfforts()
	out := make([]string, 0, len(efforts))
	for _, effort := range efforts {
		out = append(out, string(effort))
	}
	return out
}

// enabledAdapters removes the runtimes, models and effort tiers the user
// disabled. The whoami page is the one surface that keeps disabled entries
// (annotated, so the toggle has something to switch back on); a schema that
// still offered them would let a run pick something the user opted out of.
func enabledAdapters(adapters []AdapterStatus) []AdapterStatus {
	disabled := ai.Disabled()
	if disabled.Empty() {
		return adapters
	}
	out := make([]AdapterStatus, 0, len(adapters))
	for _, a := range adapters {
		provider, known := api.ProviderByName(a.Provider)
		mode := api.RuntimeMode(a.Mode)
		if !known || disabled.Runtime(provider, mode) {
			continue
		}
		a.Models = slices.DeleteFunc(slices.Clone(a.Models), func(id string) bool {
			return disabled.Model(provider, mode, id)
		})
		a.ModelDetails = slices.DeleteFunc(slices.Clone(a.ModelDetails), func(md ai.ModelDef) bool {
			return disabled.Model(provider, mode, md.ID)
		})
		for i := range a.ModelDetails {
			a.ModelDetails[i].SupportedEfforts = disabled.Efforts(a.ModelDetails[i].SupportedEfforts)
			if disabled.Effort(a.ModelDetails[i].DefaultEffort) {
				a.ModelDetails[i].DefaultEffort = api.EffortNone
			}
		}
		a.ModelCount = len(a.Models)
		out = append(out, a)
	}
	return out
}

// runtimeArgSchemasJSON reflects each runtime's cmux "extra args" option struct
// into a JSON schema, keyed by runtime. Runtimes without cmux options
// (CLIOptionsFor fails loud) are simply absent — this is the source of truth for
// "which runtimes have args".
func runtimeArgSchemasJSON() (map[api.Runtime][]byte, error) {
	out := map[api.Runtime][]byte{}
	for _, b := range api.AllRuntimes() {
		provider, _ := api.ProviderByName(b.Provider)
		opts, err := api.CLIOptionsFor(provider, b.Mode)
		if err != nil {
			continue // runtime has no cmux args
		}
		raw, err := json.Marshal(clickyrpc.SchemaForStruct(opts))
		if err != nil {
			return nil, fmt.Errorf("reflect %s cli args: %w", b, err)
		}
		out[b] = raw
	}
	return out, nil
}

// buildRuntimesCatalog renders every runtime as a catalog entry: its provider,
// mode, kind, auth env vars, live auth/model status from the probe, and (where
// available) its args schema. This replaces the old duplicated
// claudeCLIArgs/codexCLIArgs keys.
func buildRuntimesCatalog(adapters []AdapterStatus, argsByRuntime map[api.Runtime][]byte) ([]map[string]any, error) {
	out := make([]map[string]any, 0, len(adapters))
	for _, a := range adapters {
		provider, _ := api.ProviderByName(a.Provider)
		mode := api.RuntimeMode(a.Mode)
		b := api.RuntimeOf(provider, mode)
		entry := map[string]any{
			"provider":      a.Provider,
			"mode":          a.Mode,
			"kind":          mode.Kind(),
			"authenticated": a.Authenticated,
			"ready":         a.Ready(),
			// Declared, not probed: served on every entry including an unready
			// one, so the editor can render "this runtime cannot enforce a deny"
			// rather than an empty tree that reads as "no tools here".
			"permissions": api.PermissionCapabilitiesFor(b),
		}
		if env := api.AuthEnvVars(provider, mode); len(env) > 0 {
			entry["authEnvVars"] = env
		}
		if a.Binary != "" {
			entry["binary"] = a.Binary
		}
		if a.BinaryMissing != "" {
			entry["binaryMissing"] = a.BinaryMissing
		}
		if len(a.Models) > 0 {
			entry["models"] = a.Models
		}
		if len(a.ModelDetails) > 0 {
			entry["modelDetails"] = a.ModelDetails
		}
		if a.ModelError != "" {
			entry["modelError"] = a.ModelError
		}
		if raw, ok := argsByRuntime[b]; ok {
			argsMap, err := unmarshalMap(raw)
			if err != nil {
				return nil, fmt.Errorf("decode %s args schema: %w", b, err)
			}
			entry["args"] = argsMap
		}
		out = append(out, entry)
	}
	return out, nil
}

// PromptModelCatalogEntry is one provider model and the portable runtime modes
// that can execute it. Exact adapter identities never cross this boundary.
type PromptModelCatalogEntry struct {
	ID                string    `json:"id"`
	Provider          string    `json:"provider"`
	Label             string    `json:"label"`
	CapabilitiesKnown bool      `json:"capabilitiesKnown,omitempty"`
	Reasoning         bool      `json:"reasoning"`
	Temperature       *bool     `json:"temperature,omitempty"`
	SupportedEfforts  []string  `json:"supportedEfforts,omitempty"`
	DefaultEffort     string    `json:"defaultEffort,omitempty"`
	Configured        bool      `json:"configured"`
	Modes             []string  `json:"modes"`
	Runtime           api.Model `json:"runtime"`
}

// PromptModelCatalog serves one display row per provider model, listing the
// modes that can execute it. There is nothing to collapse any more: a runtime
// IS (provider, mode), so no implementation id exists to leak.
func PromptModelCatalog(adapters []AdapterStatus) []PromptModelCatalogEntry {
	out := []PromptModelCatalogEntry{}
	rows := map[string]int{}
	for _, a := range adapters {
		provider := a.Provider
		mode := a.Mode
		for _, model := range flatModelDetails(a) {
			id := strings.TrimSpace(model.id)
			if id == "" {
				continue
			}
			key := provider + "\x00" + id
			if index, exists := rows[key]; exists {
				if a.Ready() {
					out[index].Configured = true
				}
				if !containsString(out[index].Modes, mode) {
					out[index].Modes = append(out[index].Modes, mode)
				}
				continue
			}
			label := strings.TrimSpace(model.label)
			if label == "" {
				label = id
			}
			out = append(out, PromptModelCatalogEntry{
				ID:                id,
				Label:             label,
				Provider:          provider,
				CapabilitiesKnown: model.capabilitiesKnown,
				Reasoning:         model.reasoning || modelSupportsReasoning(id),
				Temperature:       model.temperature,
				Configured:        a.Ready(),
				Modes:             []string{mode},
				Runtime:           api.Model{Name: id},
			})
			rows[key] = len(out) - 1
			if len(model.supportedEfforts) > 0 {
				values := make([]string, 0, len(model.supportedEfforts))
				for _, effort := range model.supportedEfforts {
					values = append(values, string(effort))
				}
				out[len(out)-1].SupportedEfforts = values
			}
			if model.defaultEffort != api.EffortNone {
				out[len(out)-1].DefaultEffort = string(model.defaultEffort)
			}
		}
	}
	return out
}

type flatModelDetail struct {
	id                string
	label             string
	capabilitiesKnown bool
	reasoning         bool
	temperature       *bool
	supportedEfforts  []api.Effort
	defaultEffort     api.Effort
}

func flatModelDetails(adapter AdapterStatus) []flatModelDetail {
	if len(adapter.ModelDetails) > 0 {
		out := make([]flatModelDetail, 0, len(adapter.ModelDetails))
		for _, model := range adapter.ModelDetails {
			var temperature *bool
			if model.CapabilitiesKnown {
				temperature = &model.Temperature
			}
			out = append(out, flatModelDetail{
				id:                model.ID,
				label:             model.Name,
				capabilitiesKnown: model.CapabilitiesKnown,
				reasoning:         model.Reasoning,
				temperature:       temperature,
				supportedEfforts:  model.SupportedEfforts,
				defaultEffort:     model.DefaultEffort,
			})
		}
		return out
	}
	out := make([]flatModelDetail, 0, len(adapter.Models))
	for _, id := range adapter.Models {
		out = append(out, flatModelDetail{id: id, label: id})
	}
	return out
}

func modelSupportsReasoning(id string) bool {
	token := strings.ToLower(strings.TrimSpace(id))
	if slash := strings.LastIndex(token, "/"); slash >= 0 {
		token = token[slash+1:]
	}
	token = strings.TrimPrefix(token, "models/")
	switch {
	case strings.HasPrefix(token, "claude-"),
		strings.HasPrefix(token, "opus"),
		strings.HasPrefix(token, "sonnet"),
		strings.HasPrefix(token, "haiku"),
		strings.HasPrefix(token, "gpt-"),
		strings.HasPrefix(token, "o1"),
		strings.HasPrefix(token, "o3"),
		strings.HasPrefix(token, "o4"),
		strings.HasPrefix(token, "gemini-"),
		strings.HasPrefix(token, "deepseek"):
		return true
	default:
		return false
	}
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}

func unmarshalMap(raw []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
