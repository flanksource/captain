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

// ContainsRuntimeSelector reports whether s contains a backend/mode selector such
// as "cmux:gpt-5.6-sol" or "*:sonnet-5". Comma-separated fallback lists are
// scanned item by item.
func ContainsRuntimeSelector(s string) bool {
	return registry.ContainsSelector(s)
}

// ResolveModelSelectors turns every recognizable model name into a concrete
// model/backend pair while preserving fallback semantics. Unknown model names
// require an explicit backend.
func ResolveModelSelectors(model api.Model) (api.Model, error) {
	return registry.ResolveModel(model)
}

// ResolveRuntimeSelectors expands --multi-models values into concrete runtime
// model/backend pairs. Values may be repeated and/or comma-separated.
func ResolveRuntimeSelectors(values []string, base api.Model) ([]api.Model, error) {
	return registry.ResolveMulti(values, base)
}

// NormalizeModelForBackend maps a catalog/live/provider model id onto the exact
// runtime id accepted by backend. Provider prefixes and old backend-specific
// aliases are input compatibility only; the returned value is never a family
// alias such as "opus" or a synthetic id such as "claude-agent-opus".
func NormalizeModelForBackend(backend api.Backend, model string) string {
	resolved, _ := ResolveExactModelForBackend(backend, model)
	return resolved
}
