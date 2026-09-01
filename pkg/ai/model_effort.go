package ai

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/commons/logger"
)

var modelEffortLog = logger.GetLogger("ai")

// ModelEfforts returns model-specific effort metadata when the embedded
// registry knows the exact runtime/model combination, minus the tiers the user
// has disabled.
func ModelEfforts(p *ModelProvider, mode RuntimeMode, model string) (supported []api.Effort, defaultEffort api.Effort, ok bool) {
	def, ok := RegistryModelDef(p, mode, model)
	if !ok {
		return nil, api.EffortNone, false
	}
	disabled := Disabled()
	if disabled.Effort(def.DefaultEffort) {
		def.DefaultEffort = api.EffortNone
	}
	return disabled.Efforts(append([]api.Effort(nil), def.SupportedEfforts...)), def.DefaultEffort, true
}

// ResolveModelEffort returns the executable effort for a runtime/model pair.
func ResolveModelEffort(p *ModelProvider, mode RuntimeMode, model string, effort api.Effort) (api.Effort, error) {
	return registry.ResolveEffort(p, mode, model, effort)
}

type effortValidatingProvider struct {
	provider         Provider
	configuredEffort api.Effort
}

func (p *effortValidatingProvider) GetModel() string    { return p.provider.GetModel() }
func (p *effortValidatingProvider) GetRuntime() Runtime { return p.provider.GetRuntime() }
func (p *effortValidatingProvider) Unwrap() Provider    { return p.provider }
func (p *effortValidatingProvider) request(ctx context.Context, req Request) (Request, error) {
	if req.Effort == api.EffortNone {
		req.Effort = p.configuredEffort
	}
	requested := req.Effort
	runtime := p.GetRuntime()
	provider, _ := runtime.ModelProvider()
	effective, err := ResolveModelEffort(provider, runtime.Mode, p.GetModel(), requested)
	if err != nil {
		return Request{}, err
	}
	if effective != requested {
		// Name the agent in the selector notation the user already reads in
		// dispatch lines, so a degraded-effort debug line can be matched back to
		// the run it belongs to.
		identity := LogIdentity(runtime.Mode, p.GetModel(), requested)
		if effective == api.EffortNone {
			LoggerFromContext(ctx, modelEffortLog).Debugf(
				"%s does not support reasoning effort %q; continuing without effort",
				identity, requested,
			)
		} else {
			LoggerFromContext(ctx, modelEffortLog).Debugf(
				"%s does not support reasoning effort %q; using highest supported effort %q",
				identity, requested, effective,
			)
		}
	}
	req.Effort = effective
	return req, nil
}

func (p *effortValidatingProvider) Execute(ctx context.Context, req Request) (*Response, error) {
	req, err := p.request(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.provider.Execute(ctx, req)
}

type effortValidatingStreamingProvider struct {
	*effortValidatingProvider
	streamer StreamingProvider
}

func (p *effortValidatingStreamingProvider) ExecuteStream(ctx context.Context, req Request) (<-chan Event, error) {
	req, err := p.request(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.streamer.ExecuteStream(ctx, req)
}

func withEffortValidation(provider Provider, configured api.Effort) Provider {
	base := &effortValidatingProvider{provider: provider, configuredEffort: configured}
	if streamer, ok := provider.(StreamingProvider); ok {
		return &effortValidatingStreamingProvider{effortValidatingProvider: base, streamer: streamer}
	}
	return base
}
