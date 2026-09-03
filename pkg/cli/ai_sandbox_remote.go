package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/sandbox/adapter"
)

// relocatesRun reports whether the selection replaces provider execution with
// a run on another machine.
func relocatesRun(cfg ai.Config) bool {
	if cfg.SandboxSelection == nil {
		return false
	}
	descriptor, ok := registry.SandboxFor(cfg.SandboxSelection.Kind)
	return ok && descriptor.Has(registry.CapabilityRemoteExec)
}

// remoteAwareTimeout sizes the run's deadline for where the work happens. A
// relocated run blocks on a remote agent for as long as that agent takes, so
// the local request default — sized for a model call — would kill a dispatch
// that is still progressing and report it as a failure. An explicitly declared
// timeout (--timeout or budget.timeout) always wins.
func remoteAwareTimeout(req ai.Request, cfg ai.Config, timeout time.Duration) time.Duration {
	if strings.TrimSpace(req.Budget.Timeout) != "" || !relocatesRun(cfg) {
		return timeout
	}
	return adapter.WaitTimeout(cfg.SandboxSelection.Options)
}

// renderedTimeout is remoteAwareTimeout for an already-rendered prompt, used
// by the stream and batch paths that size their own deadline. A spec that
// declares no budget.timeout gets the CLI default; one that declares an
// unparseable value is an error, not a run on a substituted deadline.
func renderedTimeout(rendered PromptRenderResult) (time.Duration, error) {
	declared, err := runtimeTimeout(rendered.Input.Budget.Timeout)
	if err != nil {
		return 0, err
	}
	if declared <= 0 {
		declared = defaultRunTimeout
	}
	return remoteAwareTimeout(rendered.Input, rendered.Config, declared), nil
}

// remoteExecProviderFor returns a provider backed by the resolved sandbox's
// RemoteExecutor when the selection has that capability, nil otherwise. This
// is the run-path branch for whole-run relocation (git-agent): it sits above
// provider construction because the adapter replaces execution, not the argv.
func remoteExecProviderFor(req *ai.Request, cfg ai.Config) (ai.Provider, error) {
	if !relocatesRun(cfg) {
		return nil, nil
	}
	selection := cfg.SandboxSelection
	sandbox, err := api.NewSandbox(*selection)
	if err != nil {
		return nil, err
	}
	executor, ok := api.SandboxAs[api.RemoteExecutor](sandbox)
	if !ok {
		_ = sandbox.Close()
		return nil, fmt.Errorf("sandbox %q declares remote execution but provides none", selection.Kind)
	}
	if _, err := sandbox.Prepare(context.Background(), req); err != nil {
		_ = sandbox.Close()
		return nil, err
	}
	return &remoteExecProvider{executor: executor, sandbox: sandbox, model: cfg.Model.Name, runtime: api.RuntimeOf(cfg.Model.Provider, cfg.Model.Mode)}, nil
}

// remoteExecProvider adapts a RemoteExecutor to ai.Provider. Streaming is
// deliberately absent — the run happens elsewhere and comes back whole; the
// buffered workflow path synthesizes events from Execute.
type remoteExecProvider struct {
	executor api.RemoteExecutor
	sandbox  api.Sandbox
	model    string
	runtime  api.Runtime
	close    sync.Once
	closeErr error
}

// bufferedOnlyProvider preserves a provider's buffered capability after
// middleware wrapping. Middleware supports streaming when its inner provider
// does, but its wrapper methods must not advertise streaming for a remote run.
type bufferedOnlyProvider struct{ ai.Provider }

func (p bufferedOnlyProvider) Unwrap() ai.Provider { return p.Provider }

func (p *remoteExecProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	return p.executor.Execute(ctx, req)
}

func (p *remoteExecProvider) GetModel() string        { return p.model }
func (p *remoteExecProvider) GetRuntime() api.Runtime { return p.runtime }

// Close releases the prepared remote sandbox. It is idempotent because both
// provider setup failures and execution completion can reach this boundary.
func (p *remoteExecProvider) Close() error {
	p.close.Do(func() {
		if p.sandbox != nil {
			p.closeErr = p.sandbox.Close()
		}
	})
	return p.closeErr
}
