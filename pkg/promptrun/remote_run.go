// Hands a whole run whose resolved sandbox runs it in an environment of its own (substrate) to
// that sandbox's RemoteRunner, and files the run from what the runner relays back.
package promptrun

import (
	"context"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
)

// RemoteRunner is the capability of a sandbox that runs a whole prompt run in an environment of
// its own: setup, every turn, verification and commits happen there. RunRemote runs in, hands its
// events, verification progress, tool calls and approvals to in's handlers as the run makes them,
// calls started with the spec the run starts with once its setup is done, and returns the run's
// result. Run hands it a run whose approvals and caller tools are already bound.
type RemoteRunner interface {
	RunRemote(ctx context.Context, in Input, started func(api.Spec) error) (Result, error)
}

func init() {
	api.RegisterSandboxCapabilityCheck(api.CapabilityRemoteRun, func(sandbox api.Sandbox) bool {
		_, ok := api.SandboxAs[RemoteRunner](sandbox)
		return ok
	})
}

// RunsWhole reports whether cfg's sandbox runs whole runs in an environment of its own, which only
// Run hands a run to: a caller that builds a provider itself must go through Run instead.
func RunsWhole(cfg ai.Config) bool {
	return cfg.SandboxSelection != nil && runsWhole(*cfg.SandboxSelection)
}

// runsWhole reports whether the selection's sandbox runs whole runs in an environment of its own.
func runsWhole(selection api.SandboxConfig) bool {
	descriptor, ok := registry.SandboxFor(selection.Kind)
	return ok && descriptor.Has(registry.CapabilityRemoteRun)
}

// remoteRunner is the sandbox that runs in whole, when in would construct its provider and its
// selection's sandbox runs whole runs; nil otherwise. The returned close releases the sandbox.
func remoteRunner(in Input) (RemoteRunner, func(), error) {
	selection := in.Config.SandboxSelection
	if selection == nil || !constructsProvider(in) || !runsWhole(*selection) {
		return nil, nil, nil
	}
	sandbox, err := api.NewSandbox(*selection)
	if err != nil {
		return nil, nil, err
	}
	// NewSandbox verified the capability.
	runner, _ := api.SandboxAs[RemoteRunner](sandbox)
	return runner, func() { _ = sandbox.Close() }, nil
}

// runRemotely hands in to runner. A recorded run is filed from what the runner relays: started
// with the spec it reports, and its events and verification progress as they arrive.
func runRemotely(ctx context.Context, in Input, runner RemoteRunner, model api.Model, rec *Recorder) (Result, error) {
	started := func(api.Spec) error { return nil }
	if rec != nil {
		if err := recordOn(&in, rec, executingRuntime(nil, model), model); err != nil {
			return Result{}, err
		}
		callerEvents := in.OnEvent
		in.OnEvent = func(iter int, ev ai.Event) {
			rec.Observe(iter, ev)
			if callerEvents != nil {
				callerEvents(iter, ev)
			}
		}
		started = func(spec api.Spec) error { return rec.Start(&spec) }
	}
	return runner.RunRemote(ctx, in, started)
}
