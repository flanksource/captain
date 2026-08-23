// Package api holds captain's root domain types — the serializable, nested
// specification of a model/agent run (Model, Cost, Budget, Memory, Permissions,
// Setup, Prompt) and the Spec that composes them. It never imports pkg/ai;
// pkg/ai re-exports the enum/value types here via aliases, so this package is
// the single source of truth for Backend/Effort/Cost.
//
// Model identity itself (Backend, Effort, Model, the compact grammar, the model
// catalog) lives one level down in pkg/api/registry and is re-exported here by
// alias — see aliases.go. Spec decoding parses model strings, so the parser has
// to sit below this package.
package api

import (
	"fmt"
	"strings"
)

// SchemaStrictness governs what captain does when a structured-output response
// fails validation against the request's JSON schema. "" uses the backend
// default, while "none" explicitly disables post-response validation.
type SchemaStrictness string

const (
	SchemaStrictnessNone     SchemaStrictness = ""
	SchemaStrictnessDisabled SchemaStrictness = "none"
	SchemaStrictnessWarning  SchemaStrictness = "warning"
	SchemaStrictnessError    SchemaStrictness = "error"
	SchemaStrictnessRetry    SchemaStrictness = "retry"
)

// AllSchemaStrictness lists the non-empty strictness modes.
func AllSchemaStrictness() []SchemaStrictness {
	return []SchemaStrictness{SchemaStrictnessDisabled, SchemaStrictnessWarning, SchemaStrictnessError, SchemaStrictnessRetry}
}

// Valid reports whether s is a recognised strictness mode (including none/"").
func (s SchemaStrictness) Valid() bool {
	switch s {
	case SchemaStrictnessNone, SchemaStrictnessDisabled, SchemaStrictnessWarning, SchemaStrictnessError, SchemaStrictnessRetry:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown strictness mode, naming the valid set.
func (s SchemaStrictness) Validate() error {
	if s.Valid() {
		return nil
	}
	return fmt.Errorf("invalid schemaStrictness %q; want one of: none, warning, error, retry", s)
}

// VerifyScope narrows a workflow's verification to the changed files vs the
// whole tree. "" defaults to all.
type VerifyScope string

const (
	VerifyScopeAll     VerifyScope = "all"
	VerifyScopeChanged VerifyScope = "changed"
)

// AllVerifyScopes lists the non-empty verification scopes.
func AllVerifyScopes() []VerifyScope {
	return []VerifyScope{VerifyScopeAll, VerifyScopeChanged}
}

// Valid reports whether s is a recognised scope (including the default "").
func (s VerifyScope) Valid() bool {
	switch s {
	case "", VerifyScopeAll, VerifyScopeChanged:
		return true
	default:
		return false
	}
}

// Validate fails loud on an unknown scope, naming the valid set.
func (s VerifyScope) Validate() error {
	if s.Valid() {
		return nil
	}
	return fmt.Errorf("invalid verify scope %q; want one of: all, changed", s)
}

// ToolPolicy is the per-tool exposure for one request, and the only tool
// permission vocabulary: auto, ask, allow, deny. It keeps the wire shape close
// to coding-agent UX.
//
// The legacy "on"/"off" spellings are accepted on decode via ParseToolPolicy,
// never stored and never emitted. "on" is deliberately not a global synonym —
// see ParseToolPolicyOptions.LegacyOn.
type ToolPolicy string

const (
	ToolPolicyAuto  ToolPolicy = "auto"
	ToolPolicyAsk   ToolPolicy = "ask"
	ToolPolicyAllow ToolPolicy = "allow"
	ToolPolicyDeny  ToolPolicy = "deny"
)

// Legacy tool-permission spellings, accepted on decode only. They are not
// ToolPolicy values: "on" resolves differently per encoding (see LegacyOn).
const (
	legacyToolOn  = "on"
	legacyToolOff = "off"
)

// ParseToolPolicyOptions configures which legacy spellings a decoder accepts.
type ParseToolPolicyOptions struct {
	// LegacyOn is what the legacy "on" spelling means in the caller's encoding.
	// The two encodings differ in arity, not in meaning:
	//
	//   - allow, where the encoding has no separate allow slot and "on" is the
	//     only way to say auto-run (spec.toolPreferences, MCP _meta, stored
	//     caller-tool credentials).
	//   - auto, where an Allow list already carries allow, leaving "on" free to
	//     mean "enabled, defer gating" (the legacy permissions.tools shape).
	//
	// The zero value rejects "on" and "off" outright.
	LegacyOn ToolPolicy
}

// ParseToolPolicy canonicalizes a tool policy, optionally accepting the legacy
// "on"/"off" spellings. "off" always means deny — both legacy encodings agree.
func ParseToolPolicy(value string, opts ParseToolPolicyOptions) (ToolPolicy, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if opts.LegacyOn != "" {
		switch normalized {
		case legacyToolOn:
			return opts.LegacyOn, true
		case legacyToolOff:
			return ToolPolicyDeny, true
		}
	}
	switch ToolPolicy(normalized) {
	case ToolPolicyAuto:
		return ToolPolicyAuto, true
	case ToolPolicyAsk:
		return ToolPolicyAsk, true
	case ToolPolicyAllow:
		return ToolPolicyAllow, true
	case ToolPolicyDeny:
		return ToolPolicyDeny, true
	default:
		return "", false
	}
}

// NormalizeToolPolicy canonicalizes a policy, rejecting the legacy spellings.
func NormalizeToolPolicy(value string) (ToolPolicy, bool) {
	return ParseToolPolicy(value, ParseToolPolicyOptions{})
}

// Valid reports whether p is a recognised runtime tool policy.
func (p ToolPolicy) Valid() bool {
	_, ok := NormalizeToolPolicy(string(p))
	return ok
}

// ApprovalDecision maps a policy to an approve/auto decision. handled is false
// only for auto, which defers to the runtime's default gate.
func (p ToolPolicy) ApprovalDecision() (require, handled bool) {
	switch policy, ok := NormalizeToolPolicy(string(p)); {
	case !ok, policy == ToolPolicyAuto:
		return false, false
	case policy == ToolPolicyAsk:
		return true, true
	default: // allow, deny — both run without an approval round-trip
		return false, true
	}
}

// ResourceMode is the enabled/disabled policy for MCP servers, plugins, and skills.
type ResourceMode string

const (
	ResourceEnabled  ResourceMode = "enabled"
	ResourceDisabled ResourceMode = "disabled"
)

// Valid reports whether m is a recognised resource mode.
func (m ResourceMode) Valid() bool {
	switch m {
	case ResourceEnabled, ResourceDisabled:
		return true
	default:
		return false
	}
}

// PermissionMode is the base permission posture (claude --permission-mode).
type PermissionMode string

const (
	PermissionDefault     PermissionMode = "default"
	PermissionPlan        PermissionMode = "plan"
	PermissionAcceptEdits PermissionMode = "acceptEdits"
	PermissionAuto        PermissionMode = "auto"
	PermissionBypass      PermissionMode = "bypassPermissions"
	PermissionDontAsk     PermissionMode = "dontAsk"
)

// AllPermissionModes lists the permission postures in canonical order. Mirrors
// the `claude --permission-mode` choices so the mapping is lossless.
func AllPermissionModes() []PermissionMode {
	return []PermissionMode{
		PermissionAcceptEdits, PermissionAuto, PermissionBypass, PermissionDefault, PermissionDontAsk, PermissionPlan,
	}
}

// Valid reports whether m is a recognised permission mode (including "").
func (m PermissionMode) Valid() bool {
	if m == "" {
		return true
	}
	for _, x := range AllPermissionModes() {
		if m == x {
			return true
		}
	}
	return false
}

// Preset is a named bundle of safety defaults applied before per-tool rules.
type Preset string

const (
	// PresetEdit applies acceptEdits + a curated Read/Edit/Write/Glob/Grep allowlist.
	PresetEdit Preset = "edit"
	// PresetBare skips hooks, skills, memory, and ambient settings.
	PresetBare Preset = "bare"
)

// Valid reports whether p is a recognised preset.
func (p Preset) Valid() bool {
	switch p {
	case PresetEdit, PresetBare:
		return true
	default:
		return false
	}
}
