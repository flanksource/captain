package registry

import (
	"fmt"
)

// ModelEfforts returns the effort tiers a backend/model pair accepts, when the
// catalog knows the combination. ok is false for models the catalog has never
// heard of, which keeps effort validation permissive for brand-new ids.
//
// The lookup is exact: callers pass an already-resolved id, and resolving again
// here would answer for a sibling model (see RegistryModelDef).
func ModelEfforts(backend Backend, model string) (supported []Effort, defaultEffort Effort, ok bool) {
	p, mode, found := ProviderFor(backend)
	if !found {
		return nil, EffortNone, false
	}
	m, found := p.Lookup(model)
	if !found || !p.availableFor(m, mode) {
		return nil, EffortNone, false
	}
	return append([]Effort(nil), m.SupportedEfforts...), m.DefaultEffort, true
}

// ResolveEffort returns the executable effort for a backend/model pair. Valid
// tiers unsupported by a known model degrade to its highest supported tier;
// models without a reasoning knob use the backend default. Unknown models retain
// the requested tier so a newer provider model is not blocked by a stale catalog.
func ResolveEffort(backend Backend, model string, effort Effort) (Effort, error) {
	if err := effort.Validate(); err != nil {
		return EffortNone, err
	}
	if effort == EffortNone {
		return EffortNone, nil
	}
	supported, _, known := ModelEfforts(backend, model)
	if !known {
		return effort, nil
	}
	for _, candidate := range supported {
		if candidate == effort {
			return effort, nil
		}
	}
	ordered := AllEfforts()
	for i := len(ordered) - 1; i >= 0; i-- {
		for _, candidate := range supported {
			if candidate == ordered[i] {
				return candidate, nil
			}
		}
	}
	if len(supported) > 0 {
		return EffortNone, fmt.Errorf("model %q on %s has no valid supported reasoning efforts", model, backend)
	}
	return EffortNone, nil
}
