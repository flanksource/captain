package ai

import (
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

// Model selectors are parsed by pkg/api/registry — the same parser that decodes
// specs and prompt frontmatter. This file used to hold a second implementation of
// the grammar (resolveSelectorPart, selectorBackend, selectorModelFamily,
// wildcardBackends, splitSelectorEffort) over its own family table, which is why
// `--model agent:sol` and `model: agent:sol` in frontmatter disagreed.

// ContainsRuntimeSelector reports whether s contains a mode selector such as
// "cmux:gpt-5.6-sol" or "*:sonnet-5". Comma-separated fallback lists are scanned
// item by item.
func ContainsRuntimeSelector(s string) bool {
	return registry.ContainsSelector(s)
}

// Resolve turns an authored selection into a concrete one and is the only place
// a runtime is decided. Afterwards:
//
//   - Name is the exact id the driver is handed — never an alias ("luna"), a
//     family name ("opus") or a namespaced id ("anthropic/claude-opus-5");
//   - Mode is non-empty and served by the provider;
//   - Provider is non-nil;
//   - Effort is validated.
//
// Nothing downstream re-derives any of these. Adapters used to normalize the
// model id again on the way out — nine of them, each discarding the failure —
// which meant the id captain recorded and the id the driver received could
// differ. Resolving an already-resolved model is a no-op, so callers on a
// boundary can apply it without checking.
func Resolve(model api.Model) (api.Model, error) {
	return registry.ResolveModel(model)
}

// ResolveMulti expands --multi-models values into concrete model/mode pairs.
// Values may be repeated and/or comma-separated.
func ResolveMulti(values []string, base api.Model) ([]api.Model, error) {
	return registry.ResolveMulti(values, base)
}
