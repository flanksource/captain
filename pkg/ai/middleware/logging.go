package middleware

import (
	"context"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/commons/logger"
)

type loggingProvider struct {
	provider ai.Provider
}

func (l *loggingProvider) GetModel() string      { return l.provider.GetModel() }
func (l *loggingProvider) GetBackend() ai.Backend { return l.provider.GetBackend() }

func (l *loggingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()

	prompt := req.Prompt
	if len(prompt) > 200 {
		prompt = prompt[:200] + "..."
	}
	logger.Debugf("[%s/%s] executing prompt (%d chars)", l.provider.GetBackend(), l.provider.GetModel(), len(req.Prompt))
	if logger.IsTraceEnabled() {
		logger.Tracef("[%s] prompt: %s", l.provider.GetModel(), prompt)
	}

	resp, err := l.provider.Execute(ctx, req)
	duration := time.Since(start)

	if err != nil {
		logger.Errorf("[%s/%s] failed after %v: %v", l.provider.GetBackend(), l.provider.GetModel(), duration, err)
		return resp, err
	}

	logger.Infof("[%s/%s] completed in %v (tokens: %d in / %d out)",
		l.provider.GetBackend(), l.provider.GetModel(), duration,
		resp.Usage.InputTokens, resp.Usage.OutputTokens)

	if logger.IsTraceEnabled() {
		text := resp.Text
		if len(text) > 500 {
			text = text[:500] + "..."
		}
		logger.Tracef("[%s] response: %s", l.provider.GetModel(), text)
	}

	return resp, nil
}

func WithLogging() Option {
	return func(p ai.Provider) (ai.Provider, error) {
		return &loggingProvider{provider: p}, nil
	}
}
