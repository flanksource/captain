package cli

import (
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

// SandboxCatalog is the descriptive sandbox surface served to the workbench:
// what each adapter does, what it can do, which runtime modes it can serve, and
// which configured backends select it.
//
// It is the descriptive sibling of injectSandboxBackendEnum, which constrains
// the SandboxRef selector to the same set of names — the same split
// buildBackendsCatalog has against injectSpecConditionals. The enum tells a
// validator what is allowed; this tells the editor what the choices mean.
type SandboxCatalog struct {
	// Default is the configured sandbox.default selector, empty when unset.
	Default string `json:"default,omitempty"`
	// Kinds are the adapter descriptors in canonical order, each carrying the
	// configured backends that select it.
	Kinds []SandboxCatalogEntry `json:"kinds"`
	// Invalid are configured backends whose kind does not resolve to an adapter.
	// They are reported rather than dropped: SandboxDefaults.Resolve refuses them
	// at run time, so a silently missing backend would look like a config that
	// never loaded instead of one that is wrong.
	Invalid []SandboxBackendEntry `json:"invalid,omitempty"`
}

// SandboxCatalogEntry is one sandbox adapter descriptor, projected for the UI.
type SandboxCatalogEntry struct {
	Kind        string `json:"kind"`
	Description string `json:"description"`
	// Capabilities are the optional behaviours the adapter declares. The editor
	// uses them to decide what to offer: an agent picker needs remote-exec, and
	// isolate-workspace conflicts with a worktree or setup checkout.
	Capabilities []string `json:"capabilities"`
	// Modes are the runtime modes the adapter can serve. A pairing outside this
	// list is a hard validation error at dispatch (registry.Sandbox.ValidateMode).
	Modes []string `json:"modes"`
	// Default reports that sandbox.default names this bare kind.
	Default  bool                  `json:"default,omitempty"`
	Backends []SandboxBackendEntry `json:"backends,omitempty"`
}

// SandboxBackendEntry is one configured backend from ~/.captain.yaml.
type SandboxBackendEntry struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Default reports that sandbox.default names this backend.
	Default bool `json:"default,omitempty"`
	// URL is the endpoint a git-agent backend dispatches through.
	URL string `json:"url,omitempty"`
	// Agents is the enrolled and pending roster, for git-agent backends only.
	Agents []GitAgentListEntry `json:"agents,omitempty"`
	// Error explains why this backend cannot be selected, when its kind is
	// missing or unknown.
	Error string `json:"error,omitempty"`
}

// injectSandboxModeConditionals constrains `mode` once a sandbox is chosen, so
// an unrunnable pairing is a schema error in the editor rather than a failure at
// dispatch. registry.Sandbox.ValidateMode already rejects these hard; this is
// the same matrix expressed where the form can see it — git-agent, for
// instance, deliberately cannot serve ModeAPI.
//
// It must APPEND: injectSpecConditionals ends by assigning specMap["allOf"], so
// running before it would silently lose these rules and running after it with
// another assignment would lose the per-backend model/effort rules.
func injectSandboxModeConditionals(specMap map[string]any, defaults captainconfig.SandboxDefaults) {
	allOf, _ := specMap["allOf"].([]any)
	kinds := sandboxSelectorKinds(defaults)
	for _, selector := range sortedKeys(kinds) {
		descriptor, ok := api.SandboxFor(kinds[selector])
		if !ok || len(descriptor.Modes) == len(api.AllRuntimeModes()) {
			// Serves every mode: there is nothing to constrain.
			continue
		}
		// "" keeps an unset mode valid — the runtime resolves it from the backend.
		modes := make([]any, 0, len(descriptor.Modes)+1)
		modes = append(modes, "")
		for _, mode := range descriptor.Modes {
			modes = append(modes, string(mode))
		}
		allOf = append(allOf, sandboxModeRule(selector, modes))
	}
	if len(allOf) > 0 {
		specMap["allOf"] = allOf
	}
}

// sandboxSelectorKinds maps every selector a user may write to the adapter it
// resolves to. A configured backend shadows a bare kind of the same name,
// because SandboxDefaults.Resolve looks in Backends first.
func sandboxSelectorKinds(defaults captainconfig.SandboxDefaults) map[string]api.SandboxKind {
	out := map[string]api.SandboxKind{}
	for _, descriptor := range api.AllSandboxes() {
		out[string(descriptor.Kind)] = descriptor.Kind
	}
	for name, backend := range defaults.Backends {
		declared := strings.TrimSpace(backend.Kind)
		if declared == "" {
			continue
		}
		if kind, ok := api.ParseSandboxKind(declared); ok {
			out[name] = kind
		}
	}
	return out
}

// sandboxModeRule matches one selector in either SandboxRef form. Both branches
// pin a type: without "type": "object" the object branch's `required` would be
// vacuously true for a string, so every selector's rule would fire at once on a
// scalar sandbox and mode would be squeezed to the intersection of all adapters.
func sandboxModeRule(selector string, modes []any) map[string]any {
	scalar := map[string]any{
		"required": []any{"sandbox"},
		"properties": map[string]any{
			"sandbox": map[string]any{"type": "string", "const": selector},
		},
	}
	object := map[string]any{
		"required": []any{"sandbox"},
		"properties": map[string]any{
			"sandbox": map[string]any{
				"type":       "object",
				"required":   []any{"backend"},
				"properties": map[string]any{"backend": map[string]any{"const": selector}},
			},
		},
	}
	return map[string]any{
		"if":   map[string]any{"anyOf": []any{scalar, object}},
		"then": map[string]any{"properties": map[string]any{"mode": map[string]any{"enum": modes}}},
	}
}

// buildSandboxCatalog projects the adapter descriptor table and the user's
// configured backends into the catalog. It is pure: buildPromptSchemaDocument
// is documented as a no-I/O assembler, so the config arrives as an argument and
// is never loaded here.
func buildSandboxCatalog(defaults captainconfig.SandboxDefaults) SandboxCatalog {
	selected := strings.TrimSpace(defaults.Default)
	catalog := SandboxCatalog{Default: selected}

	configured := map[api.SandboxKind][]SandboxBackendEntry{}
	for _, name := range sortedKeys(defaults.Backends) {
		backend := defaults.Backends[name]
		entry := SandboxBackendEntry{
			Name:    name,
			Kind:    strings.TrimSpace(backend.Kind),
			Default: name == selected,
		}
		kind, ok := api.ParseSandboxKind(entry.Kind)
		// ParseSandboxKind maps "" to SandboxNone, which is the right default for
		// an absent selector but wrong for a configured backend: a backend that
		// declares no kind is a mistake, not a request to run unconfined.
		if entry.Kind == "" {
			entry.Error = fmt.Sprintf("backend declares no kind (valid: %s)", api.SandboxKindList())
		} else if !ok {
			entry.Error = fmt.Sprintf("unknown kind %q (valid: %s)", entry.Kind, api.SandboxKindList())
		}
		if entry.Error != "" {
			catalog.Invalid = append(catalog.Invalid, entry)
			continue
		}
		if url, _ := backend.Options["url"].(string); url != "" {
			entry.URL = url
		}
		if kind == api.SandboxGitAgent {
			if roster := gitAgentRoster(backend); len(roster) > 0 {
				entry.Agents = roster
			}
		}
		configured[kind] = append(configured[kind], entry)
	}

	catalog.Kinds = make([]SandboxCatalogEntry, 0, len(api.AllSandboxes()))
	for _, descriptor := range api.AllSandboxes() {
		entry := SandboxCatalogEntry{
			Kind:         string(descriptor.Kind),
			Description:  descriptor.Description,
			Capabilities: make([]string, 0, len(descriptor.Capabilities)),
			Modes:        make([]string, 0, len(descriptor.Modes)),
			Default:      selected == string(descriptor.Kind),
			Backends:     configured[descriptor.Kind],
		}
		for _, capability := range descriptor.Capabilities {
			entry.Capabilities = append(entry.Capabilities, string(capability))
		}
		for _, mode := range descriptor.Modes {
			entry.Modes = append(entry.Modes, string(mode))
		}
		catalog.Kinds = append(catalog.Kinds, entry)
	}
	return catalog
}
