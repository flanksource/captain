package api

import (
	"fmt"
	"slices"
	"strings"
)

// PermissionConstraints are monotonic permission ceilings. Later layers may
// narrow them but cannot restore denied tools or skills, exceed the maximum
// posture, or select a sandbox outside the intersected allowlist.
type PermissionConstraints struct {
	Mode         PermissionMode   `json:"mode,omitempty" yaml:"mode,omitempty"`
	Tools        Tools            `json:"tools,omitempty" yaml:"tools,omitempty"`
	Skills       ResourcePolicies `json:"skills,omitempty" yaml:"skills,omitempty"`
	SandboxModes []SandboxKind    `json:"sandboxModes,omitempty" yaml:"sandboxModes,omitempty"`
}

// PermissionConstraintsForSpec projects the restrictive part of an authored
// spec. An explicit unsandboxed boundary permits a later layer to add isolation;
// named external backends remain limited to the two backend sandbox kinds.
func PermissionConstraintsForSpec(spec Spec) PermissionConstraints {
	constraints := PermissionConstraints{Mode: spec.Permissions.Mode}
	for tool, policy := range spec.Permissions.Tools {
		if policy == ToolPolicyDeny {
			constraints.Tools = putTool(constraints.Tools, tool, policy)
		}
	}
	for skill, mode := range spec.Permissions.Skills {
		if mode == ResourceDisabled {
			constraints.Skills = putResource(constraints.Skills, skill, mode)
		}
	}
	if spec.Sandbox == nil {
		return constraints
	}
	switch spec.Sandbox.Mode {
	case SandboxOff:
		constraints.SandboxModes = AllSandboxModes()
	case "":
		if spec.Sandbox.Backend != "" {
			constraints.SandboxModes = []SandboxKind{SandboxDocker, SandboxGitAgent}
		}
	default:
		constraints.SandboxModes = []SandboxKind{spec.Sandbox.Mode}
	}
	return constraints
}

// ConstrainSpecLayerPermissions adds the restrictions authored by a layer to
// any permission constraints the embedding host already attached to it.
func ConstrainSpecLayerPermissions(layer SpecLayer) (SpecLayer, error) {
	constraints, err := strictPermissionConstraints(layer.Constraints.Permissions, PermissionConstraintsForSpec(layer.Spec))
	if err != nil {
		return SpecLayer{}, fmt.Errorf("spec layer %q permission constraints: %w", layer.Name, err)
	}
	layer.Constraints.Permissions = constraints
	return layer, nil
}

// Validate rejects values that do not describe a restrictive floor.
func (constraints PermissionConstraints) Validate() error {
	if !constraints.Mode.Valid() {
		return fmt.Errorf("invalid permission mode %q", constraints.Mode)
	}
	if _, ok := constraints.Tools[""]; ok {
		return fmt.Errorf("permission constraint tool name is required")
	}
	for _, tool := range sortedKeys(constraints.Tools) {
		if strings.TrimSpace(tool) == "" {
			return fmt.Errorf("permission constraint tool name is required")
		}
		if constraints.Tools[tool] != ToolPolicyDeny {
			return fmt.Errorf("permission constraint for tool %q must be deny, got %q", tool, constraints.Tools[tool])
		}
	}
	if _, ok := constraints.Skills[""]; ok {
		return fmt.Errorf("permission constraint skill name is required")
	}
	for _, skill := range sortedKeys(constraints.Skills) {
		if strings.TrimSpace(skill) == "" {
			return fmt.Errorf("permission constraint skill name is required")
		}
		if constraints.Skills[skill] != ResourceDisabled {
			return fmt.Errorf("permission constraint for skill %q must be disabled, got %q", skill, constraints.Skills[skill])
		}
	}
	seen := map[SandboxKind]bool{}
	for _, mode := range constraints.SandboxModes {
		if _, ok := ParseSandboxKind(string(mode)); !ok || mode == "" {
			return fmt.Errorf("invalid sandbox mode %q in permission constraints", mode)
		}
		if seen[mode] {
			return fmt.Errorf("permission constraints repeat sandbox mode %q", mode)
		}
		seen[mode] = true
	}
	return nil
}

func strictPermissionConstraints(current, next PermissionConstraints) (PermissionConstraints, error) {
	if err := current.Validate(); err != nil {
		return PermissionConstraints{}, err
	}
	if err := next.Validate(); err != nil {
		return PermissionConstraints{}, err
	}
	mode, err := strictPermissionMode(current.Mode, next.Mode)
	if err != nil {
		return PermissionConstraints{}, err
	}
	out := current.clone()
	out.Mode = mode
	for tool, policy := range next.Tools {
		out.Tools = putTool(out.Tools, tool, policy)
	}
	for skill, resourceMode := range next.Skills {
		out.Skills = putResource(out.Skills, skill, resourceMode)
	}
	out.SandboxModes = intersectSandboxModes(current.SandboxModes, next.SandboxModes)
	if len(current.SandboxModes) > 0 && len(next.SandboxModes) > 0 && len(out.SandboxModes) == 0 {
		return PermissionConstraints{}, fmt.Errorf("allowed sandbox modes have an empty intersection")
	}
	return out, nil
}

func strictPermissionMode(current, next PermissionMode) (PermissionMode, error) {
	if current == "" {
		return next, nil
	}
	if next == "" || current == next {
		return current, nil
	}
	currentRank, currentOrdered := permissionModeRank(current)
	nextRank, nextOrdered := permissionModeRank(next)
	if !currentOrdered || !nextOrdered {
		return "", fmt.Errorf("permission modes %q and %q are incomparable constraints", current, next)
	}
	if nextRank < currentRank {
		return next, nil
	}
	return current, nil
}

func permissionModeRank(mode PermissionMode) (int, bool) {
	switch mode {
	case PermissionPlan:
		return 0, true
	case PermissionDefault:
		return 1, true
	case PermissionAcceptEdits:
		return 2, true
	case PermissionBypass:
		return 3, true
	default:
		return 0, false
	}
}

func validatePermissionConstraints(spec Spec, constraints PermissionConstraints, trace []SpecLayer) error {
	if err := constraints.Validate(); err != nil {
		return fmt.Errorf("runtime constraints: %w", err)
	}
	if constraints.Mode != "" && !permissionModeWithin(spec.Permissions.Mode, constraints.Mode) {
		return permissionConstraintError(trace, "permissions.mode", string(spec.Permissions.Mode), string(constraints.Mode))
	}
	for _, tool := range sortedKeys(constraints.Tools) {
		if spec.Permissions.Tools[tool] != ToolPolicyDeny {
			return permissionConstraintError(trace, "permissions.tools."+tool, string(spec.Permissions.Tools[tool]), string(ToolPolicyDeny))
		}
	}
	for _, skill := range sortedKeys(constraints.Skills) {
		if spec.Permissions.Skills[skill] != ResourceDisabled {
			return permissionConstraintError(trace, "permissions.skills."+skill, string(spec.Permissions.Skills[skill]), string(ResourceDisabled))
		}
	}
	if len(constraints.SandboxModes) > 0 {
		actual := SandboxOff
		if spec.Sandbox != nil && spec.Sandbox.Mode != "" {
			actual = spec.Sandbox.Mode
		}
		if !slices.Contains(constraints.SandboxModes, actual) {
			allowed := make([]string, len(constraints.SandboxModes))
			for i, mode := range constraints.SandboxModes {
				allowed[i] = string(mode)
			}
			return permissionConstraintError(trace, "sandbox.mode", string(actual), strings.Join(allowed, ","))
		}
	}
	return nil
}

func permissionModeWithin(actual, limit PermissionMode) bool {
	if actual == limit {
		return true
	}
	actualRank, actualOrdered := permissionModeRank(actual)
	limitRank, limitOrdered := permissionModeRank(limit)
	return actualOrdered && limitOrdered && actualRank <= limitRank
}

func permissionConstraintError(trace []SpecLayer, field, actual, constraint string) error {
	err := &RuntimeConstraintError{Violation: RuntimeConstraintPermission, Field: field, Actual: actual, Constraint: constraint}
	if layer, ok := lastSpecFieldLayer(trace, field); ok {
		err.ActualLayer = layer.Name
	}
	if layer, ok := lastConstraintFieldLayer(trace, field, actual); ok {
		err.ConstraintLayer = layer.Name
		err.ConstraintSource = layer.Source
	}
	return err
}

func lastSpecFieldLayer(trace []SpecLayer, field string) (SpecLayer, bool) {
	for i := len(trace) - 1; i >= 0; i-- {
		layer := trace[i]
		if specFieldPresent(layer.Spec, field) {
			return layer, true
		}
		switch {
		case field == "permissions.mode" && layer.Spec.Permissions.Mode != "":
			return layer, true
		case strings.HasPrefix(field, "permissions.tools."):
			if _, ok := layer.Spec.Permissions.Tools[strings.TrimPrefix(field, "permissions.tools.")]; ok {
				return layer, true
			}
		case strings.HasPrefix(field, "permissions.skills."):
			if _, ok := layer.Spec.Permissions.Skills[strings.TrimPrefix(field, "permissions.skills.")]; ok {
				return layer, true
			}
		case field == "sandbox.mode" && layer.Spec.Sandbox != nil:
			return layer, true
		}
	}
	return SpecLayer{}, false
}

func specFieldPresent(spec Spec, field string) bool {
	path := "/" + strings.ReplaceAll(field, ".", "/")
	for present := range spec.Fields() {
		if present == path || strings.HasPrefix(path, present+"/") {
			return true
		}
	}
	return false
}

func lastConstraintFieldLayer(trace []SpecLayer, field, actual string) (SpecLayer, bool) {
	for i := len(trace) - 1; i >= 0; i-- {
		layer := trace[i]
		constraints := layer.Constraints.Permissions
		switch {
		case field == "permissions.mode" && constraints.Mode != "" && !permissionModeWithin(PermissionMode(actual), constraints.Mode):
			return layer, true
		case strings.HasPrefix(field, "permissions.tools.") && constraints.Tools[strings.TrimPrefix(field, "permissions.tools.")] == ToolPolicyDeny:
			return layer, true
		case strings.HasPrefix(field, "permissions.skills.") && constraints.Skills[strings.TrimPrefix(field, "permissions.skills.")] == ResourceDisabled:
			return layer, true
		case field == "sandbox.mode" && len(constraints.SandboxModes) > 0 && !slices.Contains(constraints.SandboxModes, SandboxKind(actual)):
			return layer, true
		}
	}
	return SpecLayer{}, false
}

func intersectSandboxModes(current, next []SandboxKind) []SandboxKind {
	if len(current) == 0 {
		return append([]SandboxKind(nil), next...)
	}
	if len(next) == 0 {
		return append([]SandboxKind(nil), current...)
	}
	out := make([]SandboxKind, 0, len(current))
	for _, mode := range current {
		if slices.Contains(next, mode) {
			out = append(out, mode)
		}
	}
	return out
}

func (constraints PermissionConstraints) clone() PermissionConstraints {
	return PermissionConstraints{
		Mode: constraints.Mode, Tools: putTools(nil, constraints.Tools), Skills: putResources(nil, constraints.Skills),
		SandboxModes: append([]SandboxKind(nil), constraints.SandboxModes...),
	}
}

func putTools(target, source Tools) Tools {
	for name, policy := range source {
		target = putTool(target, name, policy)
	}
	return target
}

func putTool(target Tools, name string, policy ToolPolicy) Tools {
	if target == nil {
		target = Tools{}
	}
	target[name] = policy
	return target
}

func putResources(target, source ResourcePolicies) ResourcePolicies {
	for name, mode := range source {
		target = putResource(target, name, mode)
	}
	return target
}

func putResource(target ResourcePolicies, name string, mode ResourceMode) ResourcePolicies {
	if target == nil {
		target = ResourcePolicies{}
	}
	target[name] = mode
	return target
}
