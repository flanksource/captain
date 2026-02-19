package middleware

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/commons/logger"
)

type Cache interface {
	Get(key string) (string, bool)
	Set(key string, value string, ttl time.Duration)
}

type cachingProvider struct {
	provider ai.Provider
	cache    Cache
	ttl      time.Duration
}

func (c *cachingProvider) GetModel() string      { return c.provider.GetModel() }
func (c *cachingProvider) GetBackend() ai.Backend { return c.provider.GetBackend() }

func (c *cachingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if ShouldBypassCache(ctx) {
		return c.provider.Execute(ctx, req)
	}

	key := cacheKey(req.Prompt, c.provider.GetModel())

	if cached, ok := c.cache.Get(key); ok {
		logger.Infof("[%s/%s] cache hit", c.provider.GetBackend(), c.provider.GetModel())
		return &ai.Response{
			Text:     cached,
			Model:    c.provider.GetModel(),
			Backend:  c.provider.GetBackend(),
			CacheHit: true,
		}, nil
	}

	logger.Debugf("[%s/%s] cache miss", c.provider.GetBackend(), c.provider.GetModel())

	resp, err := c.provider.Execute(ctx, req)
	if err != nil {
		return resp, err
	}

	if resp.Text != "" {
		c.cache.Set(key, resp.Text, c.ttl)
	}

	return resp, nil
}

func cacheKey(prompt, model string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s", prompt, model)))
	return fmt.Sprintf("%x", h)
}

func WithCache(cache Cache, ttl time.Duration) Option {
	return func(p ai.Provider) (ai.Provider, error) {
		return &cachingProvider{provider: p, cache: cache, ttl: ttl}, nil
	}
}
