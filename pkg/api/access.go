package api

import (
	"fmt"
	"slices"
	"strings"
)

// AccessDialect names the vocabulary an access intent was written in, and the
// one a runtime natively speaks: claude restricts per tool, codex through its
// sandbox and approval policy, gemini through an approval mode.
type AccessDialect string

const (
	AccessDialectPortable AccessDialect = "portable"
	AccessDialectTools    AccessDialect = "tools"
	AccessDialectSandbox  AccessDialect = "sandbox"
	AccessDialectApproval AccessDialect = "approval"
)

// AccessState is the resolved access intent and where it came from, so
// `captain permissions explain` can name the layer and field that decided it.
type AccessState struct {
	Level   AccessLevel   `json:"level"`
	Dialect AccessDialect `json:"dialect"`
	// Source is the field that set the state, e.g. permissions.access or
	// permissions.tools.
	Source string `json:"source"`
	// Layer is the trace layer that set it; empty for a spec compiled without
	// layer resolution.
	Layer string `json:"layer,omitempty"`
}

// ReadOnly reports whether the state restricts the run to read-only access.
func (s *AccessState) ReadOnly() bool { return s != nil && s.Level == AccessReadOnly }

// readOnlyDeniedAliases are the shared aliases whose every carrier a read-only
// run must not reach. A tool-deny list covering all of them is the tool
// dialect's spelling of read-only.
var readOnlyDeniedAliases = []string{"edit", "write", "shell"}

// writeAliases are the carriers a read-only sandbox already confines on codex
// but claude's sandbox does not, because it only wraps Bash.
var writeAliases = []string{"edit", "write"}

// claudeSubAgentTools spawn agents that would not inherit a per-tool deny list.
var claudeSubAgentTools = []string{"Agent", "Task", "Workflow"}

// EffectiveAccess is the access intent a runtime compiles: the layer-resolved
// state when the spec went through ResolveSpecLayers, otherwise the spec's own
// permissions.access, otherwise none.
func (p Permissions) EffectiveAccess() *AccessState {
	if p.AccessState != nil {
		return p.AccessState
	}
	if p.Access == "" {
		return nil
	}
	return &AccessState{Level: p.Access, Dialect: AccessDialectPortable, Source: "permissions.access"}
}

// ResolveAccessState walks ordered layers and keeps one access state, the later
// layer winning. A layer that sets permissions.access sets it outright; a layer
// whose own fields convert exactly (a tool-deny list covering every edit, write
// and shell carrier, or a native read-only sandbox) derives it; a layer that
// loosens a read-only state lifts it to full. Precedence is decided here, layer
// by layer, because the flattened spec no longer knows which layer wrote what.
func ResolveAccessState(layers []SpecLayer) (*AccessState, error) {
	var state *AccessState
	for _, layer := range layers {
		next, err := layerAccessState(layer.Spec, state)
		if err != nil {
			return nil, fmt.Errorf("spec layer %q: %w", layer.Name, err)
		}
		if next != state && next != nil && next.Layer == "" {
			next.Layer = layer.Name
		}
		state = next
	}
	return state, nil
}

func layerAccessState(spec Spec, current *AccessState) (*AccessState, error) {
	if resolved := spec.Permissions.AccessState; resolved != nil {
		// An already-resolved spec re-entering as one layer keeps the verdict
		// its own layers reached.
		inherited := *resolved
		return &inherited, nil
	}
	if level := spec.Permissions.Access; level != "" {
		if allowed := toolsAllowing(spec.Permissions.Tools, readOnlyDeniedAliases); level == AccessReadOnly && len(allowed) > 0 {
			return nil, fmt.Errorf("permissions.access read-only contradicts permissions.tools %s: allow in the same layer",
				strings.Join(allowed, ", "))
		}
		return &AccessState{Level: level, Dialect: AccessDialectPortable, Source: "permissions.access"}, nil
	}
	if derived := derivedAccessState(spec); derived != nil {
		return derived, nil
	}
	if loosened := accessLoosening(spec); current.ReadOnly() && loosened != "" {
		return &AccessState{Level: AccessFull, Dialect: current.Dialect, Source: loosened}, nil
	}
	return current, nil
}

// derivedAccessState converts one layer's dialect-specific fields to read-only
// when they say exactly that. permissions.mode plan never converts: it permits
// the plan-file write.
func derivedAccessState(spec Spec) *AccessState {
	if toolsDenyAll(spec.Permissions.Tools, readOnlyDeniedAliases) {
		return &AccessState{Level: AccessReadOnly, Dialect: AccessDialectTools, Source: "permissions.tools"}
	}
	if nativeReadOnlySandbox(spec.Sandbox) {
		return &AccessState{Level: AccessReadOnly, Dialect: AccessDialectSandbox, Source: "sandbox.policy.filesystem.access"}
	}
	return nil
}

// accessLoosening names the first field of one spec that grants what read-only
// forbids, or "" when it grants nothing of the kind.
func accessLoosening(spec Spec) string {
	if allowed := toolsAllowing(spec.Permissions.Tools, readOnlyDeniedAliases); len(allowed) > 0 {
		return fmt.Sprintf("permissions.tools %s: allow", strings.Join(allowed, ", "))
	}
	if ref := spec.Sandbox; ref != nil && ref.Mode != "" && ref.Mode != SandboxNative {
		return fmt.Sprintf("sandbox mode %s", ref.Mode)
	}
	if fs := sandboxFilesystem(spec.Sandbox); fs != nil && fs.Access == SandboxFilesystemWorkspaceWrite {
		return "sandbox.policy.filesystem.access workspace-write"
	}
	return ""
}

func nativeReadOnlySandbox(ref *SandboxRef) bool {
	fs := sandboxFilesystem(ref)
	return ref != nil && ref.Mode == SandboxNative && fs != nil && fs.Access == SandboxFilesystemReadOnly
}

func sandboxFilesystem(ref *SandboxRef) *SandboxFilesystemPolicy {
	if ref == nil || ref.Policy == nil {
		return nil
	}
	return ref.Policy.Filesystem
}

// toolsDenyAll reports whether tools, read in the tool dialect's own vocabulary
// (claude's), deny every tool carrying one of the aliases outright. A patterned
// deny (`Bash(rm:*)`) restricts part of a tool and does not count.
func toolsDenyAll(tools Tools, aliases []string) bool {
	if len(tools) == 0 {
		return false
	}
	resolved, _ := tools.resolveForRuntime(Anthropic, ModeCLI)
	for _, name := range aliasCarriers(agentTools["claude"], aliases) {
		if resolved[name].policy != ToolPolicyDeny {
			return false
		}
	}
	return true
}

// toolsAllowing lists the claude-vocabulary rules that allow a tool carrying
// one of the aliases, patterned or not.
func toolsAllowing(tools Tools, aliases []string) []string {
	if len(tools) == 0 {
		return nil
	}
	carriers := aliasCarriers(agentTools["claude"], aliases)
	resolved, _ := tools.resolveForRuntime(Anthropic, ModeCLI)
	var out []string
	for _, rule := range sortedKeys(resolved) {
		base, _, _ := strings.Cut(rule, "(")
		if resolved[rule].policy == ToolPolicyAllow && slices.Contains(carriers, base) {
			out = append(out, resolved[rule].describe(rule))
		}
	}
	return out
}
