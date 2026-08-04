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
	// Kind selects the adapter. Empty means SandboxNone.
	Kind SandboxKind `json:"kind" yaml:"kind"`
	// Name is the configured backend this config came from (e.g. "prod-pool"),
	// empty for ad-hoc construction. It exists for errors and logs.
	Name string `json:"name,omitempty" yaml:"name,omitempty"`
	// Options carries the kind-specific settings verbatim. Each adapter decodes
	// its own; an unknown key is the adapter's error to raise, not this layer's.
	Options map[string]any `json:"options,omitempty" yaml:"options,omitempty"`
}

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
	if _, ok := registry.SandboxFor(kind); !ok {
		panic(fmt.Sprintf("RegisterSandbox: kind %q has no descriptor in pkg/api/registry", kind))
	}
	sandboxFactoriesMu.Lock()
	defer sandboxFactoriesMu.Unlock()
	sandboxFactories[kind] = factory
}

// NewSandbox constructs the registered adapter for cfg's kind and verifies the
// instance against its descriptor: a declared capability whose seam interface
// lives in this package (wrap-command, remote-exec) and is not implemented is
// a construction error. Capabilities whose seams live elsewhere
// (isolate-workspace is pkg/ai/agent's; egress-proxy is provided by the
// confinement runtime itself) are declarations for THOSE seams to verify —
// this check deliberately does not pretend to cover them.
func NewSandbox(cfg SandboxConfig) (Sandbox, error) {
	kind := cfg.Kind
	if kind == "" {
		kind = SandboxNone
	}
	descriptor, ok := SandboxFor(kind)
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

// sandboxCapabilityChecks maps each capability with an api-level interface to
// its type assertion. Capabilities whose interfaces live above pkg/api
// (workspace isolation is pkg/ai/agent's; the egress proxy has no interface
// yet) are declared in the descriptor and verified at their own seam.
var sandboxCapabilityChecks = map[SandboxCapability]func(Sandbox) bool{
	CapabilityWrapCommand: func(s Sandbox) bool { _, ok := SandboxAs[CommandWrapper](s); return ok },
	CapabilityRemoteExec:  func(s Sandbox) bool { _, ok := SandboxAs[RemoteExecutor](s); return ok },
}

func verifySandboxCapabilities(descriptor *SandboxDescriptor, sandbox Sandbox) error {
	for _, capability := range descriptor.Capabilities {
		check, ok := sandboxCapabilityChecks[capability]
		if !ok {
			continue
		}
		if !check(sandbox) {
			return fmt.Errorf("sandbox %q declares capability %q but its adapter does not implement it",
				descriptor.Kind, capability)
		}
	}
	return nil
}
