package registry

import (
	"fmt"
	"slices"
)

// ModelEfforts returns the effort tiers a backend/model pair accepts, when the
// catalog knows the combination. ok is false for models the catalog has never
// heard of, which keeps effort validation permissive for brand-new ids.
//
// Tiers the user has disabled are removed, and a disabled default degrades to
// the nearest enabled tier, so every menu built from this is already pruned.
//
// The lookup is exact: callers pass an already-resolved id, and resolving again
// here would answer for a sibling model (see RegistryModelDef).
func ModelEfforts(backend Backend, model string) (supported []Effort, defaultEffort Effort, ok bool) {
	raw, def, found := catalogEfforts(backend, model)
	if !found {
		return nil, EffortNone, false
	}
	enabled := Disabled().Efforts(raw)
	return enabled, degradeEffort(def, enabled), true
}

// catalogEfforts is ModelEfforts without the disabled filter — the raw catalog
// truth, so ResolveEffort can tell "this model has no reasoning knob" apart from
// "every tier this model supports is disabled".
func catalogEfforts(backend Backend, model string) (supported []Effort, defaultEffort Effort, ok bool) {
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
//
// A tier the user disabled is never returned: it degrades like an unsupported
// one. Only an opt-out set that leaves nothing to fall back to is an error.
func ResolveEffort(backend Backend, model string, effort Effort) (Effort, error) {
	if err := effort.Validate(); err != nil {
		return EffortNone, err
	}
	if effort == EffortNone {
		return EffortNone, nil
	}
	disabled := Disabled()
	raw, _, known := catalogEfforts(backend, model)
	if !known {
		if !disabled.Effort(effort) {
			return effort, nil
		}
		enabled := disabled.EnabledEfforts()
		if len(enabled) == 0 {
			return EffortNone, fmt.Errorf("reasoning effort %q is disabled and no tier is left enabled; re-enable one under ai.disabled.efforts", effort)
		}
		return degradeEffort(effort, enabled), nil
	}
	if len(raw) == 0 {
		return EffortNone, nil
	}
	supported := disabled.Efforts(raw)
	if len(supported) == 0 {
		return EffortNone, fmt.Errorf("every reasoning effort supported by model %q on %s is disabled; re-enable one under ai.disabled.efforts", model, backend)
	}
	if slices.Contains(supported, effort) {
		return effort, nil
	}
	ordered := AllEfforts()
	for i := len(ordered) - 1; i >= 0; i-- {
		if slices.Contains(supported, ordered[i]) {
			return ordered[i], nil
		}
	}
	return EffortNone, fmt.Errorf("model %q on %s has no valid supported reasoning efforts", model, backend)
}

// degradeEffort picks the closest allowed tier at or below effort, falling back
// to the lowest allowed tier when effort sits below all of them. EffortNone in,
// or nothing allowed, means EffortNone out.
func degradeEffort(effort Effort, allowed []Effort) Effort {
	if effort == EffortNone || len(allowed) == 0 {
		return EffortNone
	}
	if slices.Contains(allowed, effort) {
		return effort
	}
	ordered := AllEfforts()
	target := slices.Index(ordered, effort)
	best := EffortNone
	for _, candidate := range allowed {
		rank := slices.Index(ordered, candidate)
		if rank < 0 || rank > target {
			continue
		}
		if best == EffortNone || rank > slices.Index(ordered, best) {
			best = candidate
		}
	}
	if best != EffortNone {
		return best
	}
	lowest := allowed[0]
	for _, candidate := range allowed[1:] {
		if slices.Index(ordered, candidate) < slices.Index(ordered, lowest) {
			lowest = candidate
		}
	}
	return lowest
}
