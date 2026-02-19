package middleware

import (
	"context"

	"github.com/flanksource/captain/pkg/ai"
)

type Option func(ai.Provider) (ai.Provider, error)

func Wrap(provider ai.Provider, options ...Option) (ai.Provider, error) {
	var err error
	for _, option := range options {
		provider, err = option(provider)
		if err != nil {
			return nil, err
		}
	}
	return provider, nil
}

type contextKey string

const (
	noCacheKey       contextKey = "ai:nocache"
	correlationIDKey contextKey = "ai:correlation_id"
)

func WithNoCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, noCacheKey, true)
}

func ShouldBypassCache(ctx context.Context) bool {
	noCache, _ := ctx.Value(noCacheKey).(bool)
	return noCache
}

func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}

func GetCorrelationID(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey).(string)
	return id
}
