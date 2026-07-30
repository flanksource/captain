package registry

import (
	"fmt"
	"strings"
)

// SandboxKind names one sandbox adapter — the mechanism a run's agent process
// executes under. Values are frozen: they appear in .prompt frontmatter, in
// ~/.captain.yaml, and in persisted specs.
type SandboxKind string

const (
	// SandboxNone runs the agent as a bare child process on the host. It is the
	// default and the behaviour every run had before adapters existed.
	SandboxNone SandboxKind = "none"
	// SandboxSRT wraps the agent's argv with sandbox-runtime's confinement.
	SandboxSRT SandboxKind = "srt"
	// SandboxContainer wraps the agent's argv with a container invocation.
	SandboxContainer SandboxKind = "container"
	// SandboxGitAgent relocates the whole run onto another machine over git.
	// See specs/SPEC-git-agent-protocol.md.
	SandboxGitAgent SandboxKind = "git-agent"
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

// ParseSandboxKind normalizes a kind token, reporting whether it names an
// adapter. An empty token resolves to SandboxNone: absent means unsandboxed,
// which is what every run did before this field existed.
func ParseSandboxKind(s string) (SandboxKind, bool) {
	trimmed := SandboxKind(strings.ToLower(strings.TrimSpace(s)))
	if trimmed == "" {
		return SandboxNone, true
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
