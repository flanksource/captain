package ai

import (
	"context"
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/api"
)

// remoteProvider adapts a whole-run remote sandbox to the provider contract.
// Runtime-only caller-tool authority is attached to the sandbox selection and
// never projected into the serializable request.
type remoteProvider struct {
	executor api.RemoteExecutor
	sandbox  api.Sandbox
	model    string
	backend  api.Backend
	tools    bool

	prepareOnce sync.Once
	prepareErr  error
	closeOnce   sync.Once
	closeErr    error
}

func newRemoteProvider(cfg Config) (Provider, error) {
	selection := *cfg.SandboxSelection
	descriptor, ok := api.SandboxFor(selection.Kind)
	if !ok {
		return nil, fmt.Errorf("unknown sandbox kind %q", selection.Kind)
	}
	if err := descriptor.ValidateMode(cfg.Model.Backend.Mode()); err != nil {
		return nil, err
	}
	if len(selection.CallerTools) > 0 && cfg.CallerTools == nil {
		return nil, fmt.Errorf("delegated caller tools require a supervisor caller-tool endpoint")
	}
	if len(selection.CallerTools) > 0 && !api.SupportsCallerTools(cfg.Model.Backend) {
		return nil, fmt.Errorf("remote backend %q does not support delegated caller tools", cfg.Model.Backend)
	}
	selection.CallerToolEndpoint = cfg.CallerTools
	sandbox, err := api.NewSandbox(selection)
	if err != nil {
		return nil, err
	}
	executor, ok := api.SandboxAs[api.RemoteExecutor](sandbox)
	if !ok {
		_ = sandbox.Close()
		return nil, fmt.Errorf("sandbox %q declares remote execution but provides none", selection.Kind)
	}
	return &remoteProvider{
		executor: executor, sandbox: sandbox, model: cfg.Model.Name,
		backend: cfg.Model.Backend, tools: cfg.CallerTools != nil,
	}, nil
}

func (provider *remoteProvider) Execute(ctx context.Context, request Request) (*Response, error) {
	provider.prepareOnce.Do(func() {
		_, provider.prepareErr = provider.sandbox.Prepare(ctx, &request)
	})
	if provider.prepareErr != nil {
		return nil, provider.prepareErr
	}
	return provider.executor.Execute(ctx, request)
}

func (provider *remoteProvider) ExecuteStream(ctx context.Context, request Request) (<-chan Event, error) {
	events := make(chan Event, 3)
	go func() {
		defer close(events)
		response, err := provider.Execute(ctx, request)
		if err != nil {
			emitRemoteEvent(ctx, events, Event{Kind: EventError, Error: err.Error(), Model: provider.model})
			emitRemoteEvent(ctx, events, Event{Kind: EventResult, Success: false, Error: err.Error(), Model: provider.model})
			return
		}
		if response.Text != "" {
			emitRemoteEvent(ctx, events, Event{Kind: EventText, Text: response.Text, Model: provider.model})
		}
		emitRemoteEvent(ctx, events, Event{
			Kind: EventResult, Success: true, Model: provider.model,
			Usage: &response.Usage, CostUSD: response.CostUSD,
		})
	}()
	return events, nil
}

func emitRemoteEvent(ctx context.Context, events chan<- Event, event Event) {
	select {
	case events <- event:
	case <-ctx.Done():
	}
}

func (provider *remoteProvider) GetModel() string        { return provider.model }
func (provider *remoteProvider) GetBackend() api.Backend { return provider.backend }

func (provider *remoteProvider) SupportsCallerTools() bool { return provider.tools }

func (provider *remoteProvider) Close() error {
	provider.closeOnce.Do(func() {
		provider.closeErr = provider.sandbox.Close()
	})
	return provider.closeErr
}
