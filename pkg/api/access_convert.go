package api

import (
	"fmt"
	"slices"
)

// ConversionResult is a dialect-specific permissions block rewritten into the
// portable access form. Permissions and Sandbox are the whole replacement: the
// access level plus every rule the conversion did not absorb.
type ConversionResult struct {
	Convertible bool          `json:"convertible"`
	Dialect     AccessDialect `json:"dialect,omitempty"`
	Permissions Permissions   `json:"permissions"`
	Sandbox     *SandboxRef   `json:"sandbox,omitempty"`
	// Residual describes the authored rules kept beside access, and Dropped the
	// ones read-only already implies.
	Residual []string `json:"residual,omitempty"`
	Dropped  []string `json:"dropped,omitempty"`
	// Reason says why a spec could not be converted.
	Reason string `json:"reason,omitempty"`
}

// ConvertToAccess maps a dialect-specific spec to permissions.access only when
// the result is equivalent or tighter on every runtime:
//   - tool denies covering every edit, write and shell tool become read-only,
//     with any other rule (a web deny, a read allow) kept as a residual;
//   - a native read-only sandbox with approval never (bypassPermissions)
//     becomes read-only, keeping the rest of the sandbox.
//
// permissions.mode plan is never converted: it permits the plan-file write.
func ConvertToAccess(spec Spec) ConversionResult {
	permissions := spec.Permissions
	switch {
	case permissions.Access != "":
		return ConversionResult{Reason: fmt.Sprintf("permissions.access is already %s", permissions.Access)}
	case toolsDenyAll(permissions.Tools, readOnlyDeniedAliases):
		return convertToolsDialect(spec)
	case nativeReadOnlySandbox(spec.Sandbox) && permissions.Mode == PermissionBypass:
		return convertSandboxDialect(spec)
	case permissions.Mode == PermissionPlan:
		return ConversionResult{Reason: "permissions.mode plan permits the plan-file write, so it is not read-only"}
	case nativeReadOnlySandbox(spec.Sandbox):
		return ConversionResult{Reason: "a read-only sandbox still lets a broker approve escalated writes; add permissions.mode bypassPermissions (approval never) to convert it"}
	default:
		return ConversionResult{Reason: "no exact read-only form: permissions.tools does not deny every edit, write and shell tool, and the sandbox is not native read-only"}
	}
}

func convertToolsDialect(spec Spec) ConversionResult {
	out := ConversionResult{Convertible: true, Dialect: AccessDialectTools, Permissions: spec.Permissions, Sandbox: spec.Sandbox}
	out.Permissions.Access = AccessReadOnly
	out.Permissions.AccessState = nil
	out.Permissions.Tools = nil
	carriers := aliasCarriers(agentTools["claude"], readOnlyDeniedAliases)
	for _, key := range sortedKeys(spec.Permissions.Tools) {
		policy := spec.Permissions.Tools[key]
		rule := fmt.Sprintf("permissions.tools %s: %s", key, policy)
		if policy == ToolPolicyDeny && namesOnly(Tools{key: policy}, carriers) {
			out.Dropped = append(out.Dropped, rule)
			continue
		}
		out.Permissions.Tools.put(key, policy)
		out.Residual = append(out.Residual, rule)
	}
	return out
}

// namesOnly reports whether every claude tool the rules reach is one of names.
func namesOnly(tools Tools, names []string) bool {
	resolved, _ := tools.resolveForRuntime(Anthropic, ModeCLI)
	for rule := range resolved {
		if !slices.Contains(names, rule) {
			return false
		}
	}
	return len(resolved) > 0
}

func convertSandboxDialect(spec Spec) ConversionResult {
	out := ConversionResult{Convertible: true, Dialect: AccessDialectSandbox, Permissions: spec.Permissions}
	out.Permissions.Access = AccessReadOnly
	out.Permissions.AccessState = nil
	out.Permissions.Mode = ""
	out.Dropped = []string{"permissions.mode bypassPermissions", "sandbox.policy.filesystem.access read-only"}
	ref := spec.cloneForAccess().Sandbox
	ref.Policy.Filesystem.Access = ""
	if IsEmpty(*ref.Policy.Filesystem) {
		ref.Policy.Filesystem = nil
	}
	if IsEmpty(*ref.Policy) {
		ref.Policy = nil
	}
	out.Sandbox = ref
	for _, key := range sortedKeys(spec.Permissions.Tools) {
		out.Residual = append(out.Residual, fmt.Sprintf("permissions.tools %s: %s", key, spec.Permissions.Tools[key]))
	}
	return out
}
