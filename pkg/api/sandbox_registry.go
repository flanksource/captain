package api

import (
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/api/registry"
)

// SandboxConfig selects and parameterizes one sandbox adapter. It is the
// resolved form of a `sandbox:` selection — after precedence between flag,
// frontmatter and global config has been applied.
type SandboxConfig struct {
	// Kind selects the adapter. Empty means SandboxOff.
	Kind SandboxKind `json:"kind" yaml:"kind"`
	// Name is the configured backend this config came from (e.g. "prod-pool"),
	// empty for ad-hoc construction. It exists for errors and logs.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// Options carries the kind-specific settings verbatim. Each adapter decodes
	// its own; an unknown key is the adapter's error to raise, not this layer's.
	Options map[string]any `json:"options,omitempty" yaml:"options,omitempty"`
	// Agent pins one enrolled agent of a git-agent backend, from
	// SandboxRef.Agent. Empty lets the adapter choose.
	Agent string `json:"agent,omitempty" yaml:"agent,omitempty"`
	// Dispatch is the per-run Git Agent override from SandboxRef.Dispatch.
	Dispatch *SandboxDispatchPolicy `json:"dispatch,omitempty" yaml:"dispatch,omitempty"`
}

// Options keys shared between the git-agent hook resolver and the adapters it
// constructs. They live here because the resolver (pkg/gitagent) cannot import
// the adapters (pkg/sandbox/adapter imports pkg/gitagent).
const (
	// SandboxOptionProfile selects a policy profile within one adapter kind.
	SandboxOptionProfile = "profile"
	// SandboxProfileHook is the generic exec-hook profile: the wrapped command
	// is untrusted, agent-authored repository code, so it gets its prepared
	// workspace and nothing else — no network, no provider credentials or
	// state, no host credentials (issue #40 R5.2).
	SandboxProfileHook = "hook"
	// SandboxOptionDenyRead carries extra deny-read paths ([]string) the
	// profile must hide — for hooks, the receiving repository itself.
	SandboxOptionDenyRead = "denyRead"
)

// SandboxFactory constructs a Sandbox from a SandboxConfig.
type SandboxFactory func(cfg SandboxConfig) (Sandbox, error)

// sandboxFactories is the process-global adapter registry. Unexported for the
// same reason the provider factories map is (see runtime_registry.go): mutated
// only through RegisterSandbox, read only through NewSandbox. The mutex exists
// for tests, which re-register stubs while other tests construct sandboxes;
// production writes all happen in init().
var (
	sandboxFactoriesMu sync.RWMutex
	sandboxFactories   = map[SandboxKind]SandboxFactory{}
)

// RegisterSandbox registers a factory for a kind. Adapter packages call it from
// init(). A kind with no registry descriptor panics: it means the descriptor
// table and an implementation disagree, which must fail at process start rather
// than at first use.
func RegisterSandbox(kind SandboxKind, factory SandboxFactory) {
	if _, ok := registry.SandboxAdapterFor(kind); !ok {
		panic(fmt.Sprintf("RegisterSandbox: kind %q has no descriptor in pkg/api/registry", kind))
	}
	sandboxFactoriesMu.Lock()
	defer sandboxFactoriesMu.Unlock()
	sandboxFactories[kind] = factory
}

// NewSandbox constructs the registered adapter for cfg's kind and verifies the
// instance against its descriptor. Every declared capability must have a
// construction-time checker and the instance must satisfy it; a future adapter
// cannot advertise a capability before its seam and verifier land.
func NewSandbox(cfg SandboxConfig) (Sandbox, error) {
	kind := cfg.Kind
	if kind == "" {
		kind = SandboxOff
	}
	descriptor, ok := registry.SandboxAdapterFor(kind)
	if !ok {
		return nil, fmt.Errorf("unknown sandbox kind %q; want one of: %s", kind, SandboxKindList())
	}
	sandboxFactoriesMu.RLock()
	factory, ok := sandboxFactories[kind]
	sandboxFactoriesMu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("no sandbox adapter registered for kind %q", kind)
	}
	sandbox, err := factory(cfg)
	if err != nil {
		return nil, err
	}
	if sandbox == nil {
		return nil, fmt.Errorf("sandbox adapter for kind %q returned no instance", kind)
	}
	if err := verifySandboxCapabilities(descriptor, sandbox); err != nil {
		_ = sandbox.Close()
		return nil, err
	}
	return sandbox, nil
}

// sandboxCapabilityChecks is the complete construction-time verifier table for
// capabilities adapters may currently advertise. Adding a descriptor
// capability and its behavioral seam is one change: NewSandbox fails closed
// when this table does not know how to prove the declaration.
var sandboxCapabilityChecks = map[SandboxCapability]func(Sandbox) bool{
	CapabilityWrapCommand: func(s Sandbox) bool { _, ok := SandboxAs[CommandWrapper](s); return ok },
	CapabilityRemoteExec:  func(s Sandbox) bool { _, ok := SandboxAs[RemoteExecutor](s); return ok },
	CapabilityIsolateWorkspace: func(s Sandbox) bool {
		iso, ok := SandboxAs[WorkspaceIsolating](s)
		return ok && iso.IsolatesWorkspace()
	},
	CapabilityEgressProxy: func(s Sandbox) bool {
		proxy, ok := SandboxAs[EgressProxied](s)
		return ok && proxy.ProvidesEgressProxy()
	},
}

func verifySandboxCapabilities(descriptor *SandboxDescriptor, sandbox Sandbox) error {
	for _, capability := range descriptor.Capabilities {
		check, ok := sandboxCapabilityChecks[capability]
		if !ok {
			return fmt.Errorf("sandbox %q declares capability %q but no construction-time verifier is registered",
				descriptor.Kind, capability)
		}
		if !check(sandbox) {
			return fmt.Errorf("sandbox %q declares capability %q but its adapter does not implement it",
				descriptor.Kind, capability)
		}
	}
	return nil
}
