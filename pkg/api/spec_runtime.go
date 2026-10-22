package api

import "fmt"

// ValidateRuntimeSpec checks an already-resolved primary and its fallbacks.
// Unsupported permission/resource capabilities warn; malformed states, agent
// tool-policy refusals, and unsupported sandbox isolation remain hard errors.
//
// Each candidate is checked as it will run: with its access intent compiled by
// CompileAccess for its own runtime, the same compilation the provider
// decorator applies, so a posture a fallback cannot reach is refused here
// rather than after the primary has failed.
func ValidateRuntimeSpec(spec Spec) ([]string, error) {
	var warnings []string
	for index, model := range append([]Model{spec.Model}, spec.Fallbacks...) {
		p, mode, err := model.Runtime()
		if err != nil {
			return warnings, fmt.Errorf("model %q: %w", model.Name, err)
		}
		candidate := spec
		candidate.Model = model
		candidate, report, err := CompileAccess(RuntimeOf(p, mode), candidate)
		if err != nil {
			return warnings, fmt.Errorf("model %q: %w", model.Name, err)
		}
		if err := RequireToolPolicySupport(p, mode, candidate.Permissions); err != nil {
			return warnings, fmt.Errorf("model %q: %w", model.Name, err)
		}
		sandboxWarning, err := ValidateResolvedSandbox(candidate)
		if err != nil {
			return warnings, fmt.Errorf("model %q: %w", model.Name, err)
		}
		candidateWarnings := UnsupportedPermissions(candidate)
		if sandboxWarning != "" {
			candidateWarnings = append(candidateWarnings, sandboxWarning)
		}
		for _, equivalence := range report.Equivalences {
			candidateWarnings = append(candidateWarnings, "permissions.access read-only: "+equivalence)
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
