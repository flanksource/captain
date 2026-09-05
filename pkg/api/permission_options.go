package api

// SupportedPermissionModes lists the postures a runtime honours natively or by
// approximation, in canonical order. It is what a picker should offer.
func SupportedPermissionModes(p *ModelProvider, mode RuntimeMode) []PermissionMode {
	caps := PermissionCapabilitiesFor(RuntimeOf(p, mode))
	out := make([]PermissionMode, 0, len(caps.Modes))
	for _, posture := range AllPermissionModes() {
		if caps.ModeSupport(posture).Honoured() {
			out = append(out, posture)
		}
	}
	return out
}

// SupportedToolPolicies includes broker-dependent policies; callers decide
// whether a broker is available for the run.
func SupportedToolPolicies(p *ModelProvider, mode RuntimeMode, provenance ToolProvenance) []ToolPolicy {
	caps := PermissionCapabilitiesFor(RuntimeOf(p, mode))
	out := make([]ToolPolicy, 0, 4)
	for _, policy := range AllToolPolicies() {
		if s := caps.ToolPolicySupport(provenance, policy); s.Kind != SupportUnsupported {
			out = append(out, policy)
		}
	}
	return out
}

// ToolPolicyProvenances lists the sources with enforceable policies.
func ToolPolicyProvenances(p *ModelProvider, mode RuntimeMode) []ToolProvenance {
	caps := PermissionCapabilitiesFor(RuntimeOf(p, mode))
	var out []ToolProvenance
	for _, provenance := range AllToolProvenances() {
		for _, policy := range []ToolPolicy{ToolPolicyAllow, ToolPolicyDeny} {
			if caps.ToolPolicySupport(provenance, policy).Honoured() {
				out = append(out, provenance)
				break
			}
		}
	}
	return out
}

// AllToolPolicies lists the policy values in canonical order.
func AllToolPolicies() []ToolPolicy {
	return []ToolPolicy{ToolPolicyAuto, ToolPolicyAsk, ToolPolicyAllow, ToolPolicyDeny}
}
