package middleware

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
}

func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: 3,
		BaseDelay:  time.Second,
		MaxDelay:   30 * time.Second,
	}
}

type retryProvider struct {
	provider ai.Provider
	config   RetryConfig
}

func (r *retryProvider) GetModel() string       { return r.provider.GetModel() }
func (r *retryProvider) GetBackend() ai.Backend { return r.provider.GetBackend() }

func (r *retryProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	var lastErr error

	for attempt := range r.config.MaxRetries + 1 {
		resp, err := r.provider.Execute(ctx, req)
		if err == nil {
			return resp, nil
		}

		lastErr = err

		if !ai.IsRetryable(err) {
			return resp, err
		}
		if attempt >= r.config.MaxRetries {
			break
		}

		delay := r.config.BaseDelay * time.Duration(1<<uint(attempt))
		if delay > r.config.MaxDelay {
			delay = r.config.MaxDelay
		}
		jitter := time.Duration(rand.Int64N(int64(delay) / 4))
		delay += jitter

		log.Infof("[%s] retrying after %v (attempt %d/%d): %v",
			r.provider.GetModel(), delay, attempt+1, r.config.MaxRetries, err)

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}

	return nil, lastErr
}

func WithRetry(config RetryConfig) Option {
	return func(p ai.Provider) (ai.Provider, error) {
		return &retryProvider{provider: p, config: config}, nil
	}
}
