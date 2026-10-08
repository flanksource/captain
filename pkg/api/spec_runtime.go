package api

import "fmt"

// ValidateRuntimeSpec checks an already-resolved primary and its fallbacks.
// Unsupported permission/resource capabilities warn; malformed states, agent
// tool-policy refusals, and unsupported sandbox isolation remain hard errors.
func ValidateRuntimeSpec(spec Spec) ([]string, error) {
	var warnings []string
	for index, model := range append([]Model{spec.Model}, spec.Fallbacks...) {
		if _, _, err := model.Runtime(); err != nil {
			return warnings, fmt.Errorf("model %q: %w", model.Name, err)
		}
		if err := RequireToolPolicySupport(model.Provider, model.Mode, spec.Permissions); err != nil {
			return warnings, fmt.Errorf("model %q: %w", model.Name, err)
		}
		candidate := spec
		candidate.Model = model
		sandboxWarning, err := ValidateResolvedSandbox(candidate)
		if err != nil {
			return warnings, fmt.Errorf("model %q: %w", model.Name, err)
		}
		candidateWarnings := UnsupportedPermissions(candidate)
		if sandboxWarning != "" {
			candidateWarnings = append(candidateWarnings, sandboxWarning)
		}
		for _, warning := range candidateWarnings {
			if index > 0 {
				warning = fmt.Sprintf("fallback[%d] %q: %s", index-1, model.Name, warning)
			}
			warnings = append(warnings, warning)
		}
	}
	return warnings, nil
}
