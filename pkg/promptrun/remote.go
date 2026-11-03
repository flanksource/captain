// Relocates a whole run whose resolved sandbox executes remotely (git-agent):
// the adapter's RemoteExecutor serves the run in place of a local provider.
package promptrun

import (
	"context"
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

// Relocates reports whether the selection replaces provider execution with
// a run on another machine.
func Relocates(cfg ai.Config) bool {
	if cfg.SandboxSelection == nil {
		return false
	}
	descriptor, ok := registry.SandboxFor(cfg.SandboxSelection.Kind)
	return ok && descriptor.Has(registry.CapabilityRemoteExec)
}

// RemoteProvider returns a provider backed by the resolved sandbox's
// RemoteExecutor when the selection has that capability, nil otherwise. It
// sits above provider construction because the adapter replaces execution, not
// the argv. The provider carries the default middleware and never streams: the
// run happens elsewhere and comes back whole. Callers close it.
func RemoteProvider(req *ai.Request, cfg ai.Config) (ai.Provider, error) {
	if !Relocates(cfg) {
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
	remote := &remoteExecProvider{executor: executor, sandbox: sandbox, model: cfg.Model.Name, runtime: api.RuntimeOf(cfg.Model.Provider, cfg.Model.Mode)}
	wrapped, err := middleware.Wrap(remote, middleware.WithLogging(), middleware.WithJevConversion(), middleware.WithSchemaValidation(cfg))
	if err != nil {
		_ = remote.Close()
		return nil, err
	}
	return nonStreamingProvider{Provider: wrapped}, nil
}

// relocate hands a run that would construct its provider, and whose resolved
// sandbox executes remotely, to that sandbox: in.Provider becomes the remote
// provider, so Run builds no local one and adds no setup hook. The returned
// release closes it.
func relocate(in *Input) (func(), error) {
	release := func() {}
	if !constructsProvider(*in) {
		return release, nil
	}
	remote, err := RemoteProvider(&in.Resolved.Spec, in.Config)
	if err != nil || remote == nil {
		return release, err
	}
	in.Provider = remote
	return func() { closeProvider(remote) }, nil
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

// nonStreamingProvider preserves a provider's buffered capability after
// middleware wrapping. Middleware supports streaming when its inner provider
// does, but its wrapper methods must not advertise streaming for a remote run.
type nonStreamingProvider struct{ ai.Provider }

func (p nonStreamingProvider) Unwrap() ai.Provider { return p.Provider }

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
