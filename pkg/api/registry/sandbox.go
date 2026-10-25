package registry

import (
	"fmt"
	"slices"
	"strings"
)

// SandboxKind names one sandbox adapter — the mechanism a run's agent process
// executes under. Values are frozen: they appear in .prompt frontmatter, in
// ~/.captain.yaml, and in persisted specs.
type SandboxKind string

const (
	// SandboxOff disables provider-native filesystem/network isolation and
	// approval prompts. It is intentionally explicit: an absent sandbox inherits
	// the configured default, while "off" overrides that default.
	SandboxOff SandboxKind = "off"
	// SandboxNative translates Captain's provider-neutral policy into the active
	// Claude, Codex, or Gemini runtime's own sandbox controls.
	SandboxNative SandboxKind = "native"
	// SandboxDocker runs the provider inside a Docker boundary. Provider-native
	// filesystem/network isolation is disabled inside that external boundary.
	SandboxDocker SandboxKind = "docker"
	// SandboxGitAgent relocates the whole run onto another machine over git.
	// See specs/SPEC-git-agent-protocol.md.
	SandboxGitAgent SandboxKind = "git-agent"
	// SandboxSRTInternal confines Git Agent receive hooks. It is deliberately not
	// present in AllSandboxes or ParseSandboxKind and cannot be selected by a run.
	SandboxSRTInternal SandboxKind = "srt-internal"
)

// SandboxCapability names an optional behaviour an adapter may implement. The
// descriptor declares them; pkg/api resolves them by type assertion. Declaring a
// capability an adapter does not implement is a registration-time error, not a
// silent no-op at run time.
type SandboxCapability string

const (
	// CapabilityWrapCommand rewrites the agent's argv/env before exec.
	CapabilityWrapCommand SandboxCapability = "wrap-command"
	// CapabilityRemoteExec replaces local execution entirely.
	CapabilityRemoteExec SandboxCapability = "remote-exec"
	// CapabilityIsolateWorkspace materializes its own working tree, and so
	// conflicts with --worktree and with a setup checkout.
	CapabilityIsolateWorkspace SandboxCapability = "isolate-workspace"
	// CapabilityEgressProxy substitutes placeholder credentials for real ones on
	// outbound HTTP. See specs/SPEC-git-agent-protocol.md §9.
	CapabilityEgressProxy SandboxCapability = "egress-proxy"
)

// ToolPlacement is where a run's inline tool executes relative to its sandbox.
type ToolPlacement string

const (
	// ToolPlacementHost runs the tool next to Captain, outside the run's
	// sandbox: the sandbox confines the agent, never the tool.
	ToolPlacementHost ToolPlacement = "host"
	// ToolPlacementSandbox runs the tool inside the run's sandbox, through the
	// adapter's command wrapper.
	ToolPlacementSandbox ToolPlacement = "sandbox"
	// ToolPlacementSupervisor runs a relocated run's tool on the supervisor, the
	// host the run was submitted from; the remote agent reaches it over the
	// git-agent tunnel.
	ToolPlacementSupervisor ToolPlacement = "supervisor"
	// ToolPlacementSidecar runs a relocated run's tool on the git-agent sidecar,
	// next to the agent's worktree.
	ToolPlacementSidecar ToolPlacement = "sidecar"
)

// Valid reports whether p names a placement; empty is valid and means the
// sandbox's default (see Sandbox.ResolveToolPlacement).
func (p ToolPlacement) Valid() bool {
	switch p {
	case "", ToolPlacementHost, ToolPlacementSandbox, ToolPlacementSupervisor, ToolPlacementSidecar:
		return true
	}
	return false
}

// Confined reports whether the tool runs away from Captain's own process —
// inside the run's sandbox or on a sidecar — where secrets go only when the
// prompt says how.
func (p ToolPlacement) Confined() bool {
	return p == ToolPlacementSandbox || p == ToolPlacementSidecar
}

// Sandbox describes one sandbox adapter: what it can do and which runtime modes
// it can serve.
//
// It is a struct rather than an interface for the same reason Provider is (see
// provider.go): the instances are entirely data, and an interface would invite
// each adapter to answer these questions its own way. The behaviour lives in
// pkg/api's Sandbox interface; this type is only the declaration of what that
// behaviour is allowed to be.
type Sandbox struct {
	// Kind is the canonical selector written in config and frontmatter.
	Kind SandboxKind
	// Description is one line of help text, shown by `captain sandbox` and in the
	// generated JSON schema.
	Description string
	// Capabilities are the optional interfaces this adapter implements.
	Capabilities []SandboxCapability
	// Modes are the runtime modes this adapter can serve. A run whose backend
	// resolves to a mode outside this list is a validation error — the adapter
	// has no seam to act on, and silently running unsandboxed is the failure this
	// field exists to prevent.
	Modes []RuntimeMode
	// ToolPlacements are where a run under this adapter can execute its inline
	// tools, the default first. A placement outside the list is refused before
	// the run starts: a tool the agent cannot reach, or one silently run outside
	// the boundary it asked for, is the failure this field exists to prevent.
	ToolPlacements []ToolPlacement
}

// ResolveToolPlacement resolves an unset placement to the adapter's default
// and fails loud when the adapter cannot run a tool where it asks to run,
// naming what it can do instead.
func (s *Sandbox) ResolveToolPlacement(placement ToolPlacement) (ToolPlacement, error) {
	if len(s.ToolPlacements) == 0 {
		return "", fmt.Errorf("sandbox %q cannot run inline tools", s.Kind)
	}
	if placement == "" {
		return s.ToolPlacements[0], nil
	}
	if slices.Contains(s.ToolPlacements, placement) {
		return placement, nil
	}
	supported := make([]string, len(s.ToolPlacements))
	for i, p := range s.ToolPlacements {
		supported[i] = string(p)
	}
	return "", fmt.Errorf("sandbox %q cannot run an inline tool with placement %q; it supports: %s",
		s.Kind, placement, strings.Join(supported, ", "))
}

// Has reports whether the adapter declares a capability.
func (s *Sandbox) Has(capability SandboxCapability) bool {
	for _, declared := range s.Capabilities {
		if declared == capability {
			return true
		}
	}
	return false
}

// SupportsMode reports whether the adapter can serve a runtime mode.
func (s *Sandbox) SupportsMode(mode RuntimeMode) bool {
	for _, supported := range s.Modes {
		if supported == mode {
			return true
		}
	}
	return false
}

// ValidateMode fails loud when an adapter cannot serve a mode, naming what it
// can serve. Callers reach it through Spec validation, so an unsupported pairing
// is rejected before a run starts rather than degrading to no sandbox at all.
func (s *Sandbox) ValidateMode(mode RuntimeMode) error {
	if s.SupportsMode(mode) {
		return nil
	}
	supported := make([]string, len(s.Modes))
	for i, m := range s.Modes {
		supported[i] = string(m)
	}
	return fmt.Errorf("sandbox %q does not support runtime mode %q; it supports: %s",
		s.Kind, mode, strings.Join(supported, ", "))
}

// SandboxFor returns the descriptor for a kind, reporting whether it is known.
func SandboxFor(kind SandboxKind) (*Sandbox, bool) {
	for _, s := range AllSandboxes() {
		if s.Kind == kind {
			return s, true
		}
	}
	return nil, false
}

// SandboxAdapterFor returns descriptors for both selectable modes and internal
// adapters. Construction uses this wider registry; schemas and selectors use
// SandboxFor so internal confinement mechanisms never leak into user config.
func SandboxAdapterFor(kind SandboxKind) (*Sandbox, bool) {
	if kind == SandboxSRTInternal {
		return internalSRTSandbox, true
	}
	return SandboxFor(kind)
}

// ParseSandboxKind normalizes a kind token, reporting whether it names an
// adapter. An empty token resolves to SandboxOff: absent means unsandboxed,
// which is what every run did before this field existed.
func ParseSandboxKind(s string) (SandboxKind, bool) {
	trimmed := SandboxKind(strings.ToLower(strings.TrimSpace(s)))
	if trimmed == "" {
		return SandboxOff, true
	}
	if _, ok := SandboxFor(trimmed); ok {
		return trimmed, true
	}
	return "", false
}

// Valid reports whether k names a registered adapter.
func (k SandboxKind) Valid() bool {
	_, ok := SandboxFor(k)
	return ok
}

// Validate fails loud on an unknown kind, naming the valid set.
func (k SandboxKind) Validate() error {
	if k.Valid() {
		return nil
	}
	return fmt.Errorf("invalid sandbox %q; want one of: %s", k, SandboxKindList())
}

// SandboxKindList renders AllSandboxes as comma-separated text for help/errors.
func SandboxKindList() string {
	kinds := make([]string, len(AllSandboxes()))
	for i, s := range AllSandboxes() {
		kinds[i] = string(s.Kind)
	}
	return strings.Join(kinds, ", ")
}
