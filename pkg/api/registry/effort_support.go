package registry

import (
	"fmt"
	"strings"
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

// ValidateEffort enforces model-aware effort tiers without requiring the catalog
// to be exhaustive: an unknown model accepts any valid tier, while a known one
// accepts only the tiers the catalog records for it.
func ValidateEffort(backend Backend, model string, effort Effort) error {
	if err := effort.Validate(); err != nil {
		return err
	}
	if effort == EffortNone {
		return nil
	}
	supported, _, known := ModelEfforts(backend, model)
	if !known {
		return nil
	}
	if len(supported) == 0 {
		return fmt.Errorf("model %q on %s does not support a reasoning effort", model, backend)
	}
	for _, candidate := range supported {
		if candidate == effort {
			return nil
		}
	}
	values := make([]string, 0, len(supported))
	for _, candidate := range supported {
		values = append(values, string(candidate))
	}
	return fmt.Errorf("model %q on %s does not support reasoning effort %q; want one of: %s",
		model, backend, effort, strings.Join(values, ", "))
}
