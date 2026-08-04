package cli

import (
	"context"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

// remoteExecProviderFor returns a provider backed by the resolved sandbox's
// RemoteExecutor when the selection has that capability, nil otherwise. This
// is the run-path branch for whole-run relocation (git-agent): it sits above
// provider construction because the adapter replaces execution, not the argv.
func remoteExecProviderFor(req *ai.Request, cfg ai.Config) (ai.Provider, error) {
	selection := cfg.SandboxSelection
	if selection == nil {
		return nil, nil
	}
	descriptor, ok := registry.SandboxFor(selection.Kind)
	if !ok || !descriptor.Has(registry.CapabilityRemoteExec) {
		return nil, nil
	}
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
	return &remoteExecProvider{executor: executor, sandbox: sandbox, model: cfg.Model.Name, backend: cfg.Model.Backend}, nil
}

// remoteExecProvider adapts a RemoteExecutor to ai.Provider. Streaming is
// deliberately absent — the run happens elsewhere and comes back whole; the
// buffered workflow path synthesizes events from Execute.
type remoteExecProvider struct {
	executor api.RemoteExecutor
	sandbox  api.Sandbox
	model    string
	backend  api.Backend
}

func (p *remoteExecProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	defer p.sandbox.Close()
	return p.executor.Execute(ctx, req)
}

func (p *remoteExecProvider) GetModel() string        { return p.model }
func (p *remoteExecProvider) GetBackend() api.Backend { return p.backend }
