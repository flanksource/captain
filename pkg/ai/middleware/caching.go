package middleware

import (
	"context"
	"errors"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/cache"
	"github.com/flanksource/commons/logger"
)

type cachingProvider struct {
	provider ai.Provider
	cache    *cache.Cache
}

func (c *cachingProvider) GetModel() string       { return c.provider.GetModel() }
func (c *cachingProvider) GetBackend() ai.Backend { return c.provider.GetBackend() }

func (c *cachingProvider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	if ShouldBypassCache(ctx) {
		return c.provider.Execute(ctx, req)
	}

	entry, err := c.cache.Get(req.Prompt, c.provider.GetModel())
	if err == nil && entry != nil && entry.Error == "" {
		logger.Infof("[%s/%s] cache hit", c.provider.GetBackend(), c.provider.GetModel())
		return &ai.Response{
			Text:     entry.Response,
			Model:    entry.Model,
			Backend:  c.provider.GetBackend(),
			CacheHit: true,
			Usage: ai.Usage{
				InputTokens:      entry.TokensInput,
				OutputTokens:     entry.TokensOutput,
				ReasoningTokens:  entry.TokensReasoning,
				CacheReadTokens:  entry.TokensCacheRead,
				CacheWriteTokens: entry.TokensCacheWrite,
			},
		}, nil
	} else if err != nil && !errors.Is(err, cache.ErrNotFound) && !errors.Is(err, cache.ErrCacheDisabled) {
		return nil, err
	}

	logger.Debugf("[%s/%s] cache miss", c.provider.GetBackend(), c.provider.GetModel())

	start := time.Now()
	resp, execErr := c.provider.Execute(ctx, req)
	duration := time.Since(start)

	cacheEntry := &cache.Entry{
		Model:      c.provider.GetModel(),
		Prompt:     req.Prompt,
		DurationMS: duration.Milliseconds(),
		Provider:   string(c.provider.GetBackend()),
	}

	if execErr != nil {
		cacheEntry.Error = execErr.Error()
		_ = c.cache.Set(cacheEntry)
		return resp, execErr
	}

	cacheEntry.Response = resp.Text
	cacheEntry.TokensInput = resp.Usage.InputTokens
	cacheEntry.TokensOutput = resp.Usage.OutputTokens
	cacheEntry.TokensReasoning = resp.Usage.ReasoningTokens
	cacheEntry.TokensCacheRead = resp.Usage.CacheReadTokens
	cacheEntry.TokensCacheWrite = resp.Usage.CacheWriteTokens
	cacheEntry.TokensTotal = resp.Usage.TotalTokens()
	cacheEntry.MaxTokens = req.MaxTokens
	cacheEntry.Temperature = req.Temperature

	_ = c.cache.Set(cacheEntry)
	return resp, nil
}

func WithCache(configs ...cache.Config) Option {
	return func(p ai.Provider) (ai.Provider, error) {
		var cfg cache.Config
		if len(configs) > 0 {
			cfg = configs[0]
		} else {
			cfg = cache.Config{TTL: 7 * 24 * time.Hour}
		}
		c, err := cache.New(cfg)
		if err != nil {
			return nil, err
		}
		return &cachingProvider{provider: p, cache: c}, nil
	}
}

func WithCacheInstance(c *cache.Cache) Option {
	return func(p ai.Provider) (ai.Provider, error) {
		return &cachingProvider{provider: p, cache: c}, nil
	}
}
