package middleware

import (
	"context"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/clicky"
	"github.com/flanksource/clicky/api/icons"
	"github.com/flanksource/commons/logger"
)

type loggingProvider struct {
	provider ai.Provider
}

func (l *loggingProvider) GetModel() string      { return l.provider.GetModel() }
func (l *loggingProvider) GetBackend() ai.Backend { return l.provider.GetBackend() }

func (l *loggingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()

	logger.Debugf("%v", clicky.Text("").
		Add(icons.AI).
		Append(fmt.Sprintf(" %s/%s", l.provider.GetBackend(), l.provider.GetModel()), "text-purple-600 font-medium").
		NewLine().
		Append(req.Prompt, "text-gray-600 max-w-[100ch]"))

	resp, err := l.provider.Execute(ctx, req)
	duration := time.Since(start)

	if err != nil {
		logger.Errorf("%v", clicky.Text("").
			Add(icons.Error).
			Append(fmt.Sprintf(" %s/%s", l.provider.GetBackend(), l.provider.GetModel()), "text-red-600 font-medium").
			Append(fmt.Sprintf(" failed after %v: %v", duration.Round(time.Millisecond), err), "text-red-500"))
		return resp, err
	}

	logger.Infof("%v", clicky.Text("").
		Add(icons.Check).
		Append(fmt.Sprintf(" %s/%s", l.provider.GetBackend(), l.provider.GetModel()), "text-green-600 font-medium").
		Append(fmt.Sprintf(" %v", duration.Round(time.Millisecond)), "text-gray-500").
		Append(fmt.Sprintf(" (tokens: %d in / %d out)", resp.Usage.InputTokens, resp.Usage.OutputTokens), "text-gray-400"))

	if logger.IsTraceEnabled() {
		logger.Tracef("%v", clicky.Text("").
			Add(icons.ArrowDown).
			Append(" response", "text-gray-500").
			NewLine().
			Append(resp.Text, "text-gray-600 max-w-[100ch]"))
	}

	return resp, nil
}

func WithLogging() Option {
	return func(p ai.Provider) (ai.Provider, error) {
		return &loggingProvider{provider: p}, nil
	}
}
