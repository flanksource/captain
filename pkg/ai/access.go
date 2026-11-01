package ai

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/logger"
)

var accessLog = logger.GetLogger("ai")

// accessProvider compiles the request's access intent for the runtime this
// candidate actually runs on. It wraps every candidate — the primary and each
// fallback — so a fallback in another family speaks its own dialect rather
// than the primary's. With no access intent the request passes through as is.
type accessProvider struct {
	provider Provider
}

func (p *accessProvider) GetModel() string    { return p.provider.GetModel() }
func (p *accessProvider) GetRuntime() Runtime { return p.provider.GetRuntime() }
func (p *accessProvider) Unwrap() Provider    { return p.provider }

func (p *accessProvider) request(ctx context.Context, req Request) (Request, error) {
	compiled, report, err := api.CompileAccess(p.GetRuntime(), req)
	if err != nil {
		return Request{}, err
	}
	for _, equivalence := range report.Equivalences {
		LoggerFromContext(ctx, accessLog).Debugf("%s permissions.access read-only: %s", report.Runtime, equivalence)
	}
	return compiled, nil
}

func (p *accessProvider) Execute(ctx context.Context, req Request) (*Response, error) {
	req, err := p.request(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.provider.Execute(ctx, req)
}

type accessStreamingProvider struct {
	*accessProvider
	streamer StreamingProvider
}

func (p *accessStreamingProvider) ExecuteStream(ctx context.Context, req Request) (<-chan Event, error) {
	req, err := p.request(ctx, req)
	if err != nil {
		return nil, err
	}
	return p.streamer.ExecuteStream(ctx, req)
}

func withAccess(provider Provider) Provider {
	base := &accessProvider{provider: provider}
	if streamer, ok := provider.(StreamingProvider); ok {
		return &accessStreamingProvider{accessProvider: base, streamer: streamer}
	}
	return base
}
