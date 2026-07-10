package ai

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/api"
)

// ModelEfforts returns model-specific effort metadata when the embedded
// registry knows the exact backend/model combination.
func ModelEfforts(backend Backend, model string) (supported []api.Effort, defaultEffort api.Effort, ok bool) {
	def, ok := RegistryModelDef(backend, model)
	if !ok {
		return nil, api.EffortNone, false
	}
	return append([]api.Effort(nil), def.SupportedEfforts...), def.DefaultEffort, true
}

// ValidateModelEffort enforces model-aware effort levels without making the
// catalog exhaustive. Unknown models retain Captain's historical suggest-only
// behavior; known registry entries accept only source-backed executable tiers.
func ValidateModelEffort(backend Backend, model string, effort api.Effort) error {
	if err := effort.Validate(); err != nil {
		return err
	}
	if effort == api.EffortNone {
		return nil
	}
	supported, _, known := ModelEfforts(backend, model)
	if !known {
		return nil
	}
	if len(supported) == 0 {
		return fmt.Errorf("model %q on %s does not support a reasoning effort", model, backend)
	}
	for _, candidate := range supported {
		if candidate == effort {
			return nil
		}
	}
	values := make([]string, 0, len(supported))
	for _, candidate := range supported {
		values = append(values, string(candidate))
	}
	return fmt.Errorf("model %q on %s does not support reasoning effort %q; want one of: %s",
		model, backend, effort, strings.Join(values, ", "))
}

type effortValidatingProvider struct {
	provider         Provider
	configuredEffort api.Effort
}

func (p *effortValidatingProvider) GetModel() string    { return p.provider.GetModel() }
func (p *effortValidatingProvider) GetBackend() Backend { return p.provider.GetBackend() }
func (p *effortValidatingProvider) request(req Request) (Request, error) {
	if req.Effort == api.EffortNone {
		req.Effort = p.configuredEffort
	}
	if err := ValidateModelEffort(p.GetBackend(), p.GetModel(), req.Effort); err != nil {
		return Request{}, err
	}
	return req, nil
}

func (p *effortValidatingProvider) Execute(ctx context.Context, req Request) (*Response, error) {
	req, err := p.request(req)
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
	req, err := p.request(req)
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
