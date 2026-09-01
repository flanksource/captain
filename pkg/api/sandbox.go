package api

import (
	"context"

	"github.com/flanksource/captain/pkg/api/registry"
)

// Sandbox identity and the static descriptor table live in pkg/api/registry —
// the same split, for the same reason, as Model/Backend (see aliases.go). The
// registry.Sandbox descriptor is re-exported as SandboxDescriptor because the
// api.Sandbox name belongs to the behavioural contract below.

type (
	SandboxKind       = registry.SandboxKind
	SandboxCapability = registry.SandboxCapability
	SandboxDescriptor = registry.Sandbox
)

const (
	SandboxOff         = registry.SandboxOff
	SandboxNative      = registry.SandboxNative
	SandboxDocker      = registry.SandboxDocker
	SandboxGitAgent    = registry.SandboxGitAgent
	SandboxSRTInternal = registry.SandboxSRTInternal

	CapabilityWrapCommand      = registry.CapabilityWrapCommand
	CapabilityRemoteExec       = registry.CapabilityRemoteExec
	CapabilityIsolateWorkspace = registry.CapabilityIsolateWorkspace
	CapabilityEgressProxy      = registry.CapabilityEgressProxy
)

// ParseSandboxKind normalizes a selectable mode token; empty resolves to Off.
func ParseSandboxKind(s string) (SandboxKind, bool) { return registry.ParseSandboxKind(s) }

// SandboxFor returns the static descriptor for a kind.
func SandboxFor(kind SandboxKind) (*SandboxDescriptor, bool) { return registry.SandboxFor(kind) }

// AllSandboxes lists every sandbox adapter descriptor in canonical order.
func AllSandboxes() []*SandboxDescriptor { return registry.AllSandboxes() }

// AllSandboxModes lists the public modes in canonical selector order.
func AllSandboxModes() []SandboxKind {
	modes := make([]SandboxKind, len(AllSandboxes()))
	for i, sandbox := range AllSandboxes() {
		modes[i] = sandbox.Kind
	}
	return modes
}

// SandboxKindList renders the adapter kinds as comma-separated text for
// help/error strings.
func SandboxKindList() string { return registry.SandboxKindList() }

// Sandbox is the behavioural contract of one sandbox adapter — the mechanism a
// run's agent process executes under. Instances come from NewSandbox; what an
// instance can actually do is discovered with SandboxAs against the capability
// interfaces below, never by switching on Kind.
type Sandbox interface {
	// Kind names the adapter, matching its registry descriptor.
	Kind() SandboxKind
	// Prepare establishes per-run state for one spec. It is called once per run,
	// before execution; the returned session carries whatever the exec site must
	// apply to the agent process.
	Prepare(ctx context.Context, spec *Spec) (*SandboxSession, error)
	// Close releases state the adapter holds across runs.
	Close() error
}

// SandboxSession is the per-run state Prepare establishes. Zero-valued fields
// mean "no change": the exec site applies only what is set.
type SandboxSession struct {
	// WorkDir, when non-empty, overrides the working directory the agent process
	// runs in.
	WorkDir string
	// Env are additional environment variables (KEY=VALUE) for the agent process.
	Env []string
}

// SandboxUnwrapper is implemented by decorating adapters so SandboxAs can find
// a capability behind them. Mirrors ProviderUnwrapper.
type SandboxUnwrapper interface {
	Unwrap() Sandbox
}

// CommandWrapper is the capability of adapters that confine a local process by
// rewriting its argv/env before exec (srt, container). The returned values
// replace the originals wholesale. It takes a context and returns an error
// because wrapping may require constructing live confinement state; an adapter
// that cannot wrap MUST fail here — falling through to an unwrapped command
// would silently run unsandboxed.
type CommandWrapper interface {
	Wrap(ctx context.Context, cmd string, args, env []string) (string, []string, []string, error)
}

// RemoteExecutor is the capability of adapters that do not wrap a local exec at
// all but relocate the whole run elsewhere (git-agent). An adapter with this
// capability replaces provider execution for the run.
type RemoteExecutor interface {
	Execute(ctx context.Context, spec Spec) (*Response, error)
}

// WorkspaceIsolating marks an adapter whose run happens in its own working
// tree, so selecting it must not be combined with another workspace isolator
// (a --worktree run or a setup checkout).
type WorkspaceIsolating interface {
	IsolatesWorkspace() bool
}

// EgressProxied marks an adapter whose sandbox never holds a real credential:
// placeholders are substituted outside it by an egress proxy.
type EgressProxied interface {
	ProvidesEgressProxy() bool
}

// SandboxAs resolves a capability from a sandbox, walking SandboxUnwrapper
// chains so a capability is found behind decorators. Mirrors ProviderAs.
func SandboxAs[T any](sandbox Sandbox) (T, bool) {
	for sandbox != nil {
		if capability, ok := any(sandbox).(T); ok {
			return capability, true
		}
		wrapper, ok := sandbox.(SandboxUnwrapper)
		if !ok {
			break
		}
		sandbox = wrapper.Unwrap()
	}
	var zero T
	return zero, false
}
