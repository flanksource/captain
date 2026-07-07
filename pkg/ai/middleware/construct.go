package middleware

import (
	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/cache"
)

// DefaultOptions is captain's standard middleware stack for a Config: response
// logging is always applied, and response caching is added when cfg configures
// it (a cache TTL or DB path, with NoCache unset). This lives in middleware, not
// ai.NewProvider, because middleware imports ai (wrapping ai.Provider) and the
// reverse import would be a cycle.
func DefaultOptions(cfg ai.Config) []Option {
	opts := []Option{WithLogging(), WithSchemaValidation()}
	if !cfg.NoCache && (cfg.CacheTTL > 0 || cfg.CacheDBPath != "") {
		opts = append(opts, WithCache(cache.Config{
			DBPath:  cfg.CacheDBPath,
			TTL:     cfg.CacheTTL,
			NoCache: cfg.NoCache,
		}))
	}
	return opts
}

// NewProvider builds the provider for cfg with the default middleware stack
// (DefaultOptions) applied. It is the batteries-included counterpart to
// ai.NewProvider, which applies no middleware — every embedder that wants the
// standard logging (and configured caching) should use this instead of
// re-wrapping by hand.
func NewProvider(cfg ai.Config) (ai.Provider, error) {
	p, err := ai.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	return Wrap(p, DefaultOptions(cfg)...)
}

// NewAgent builds an ai.Agent whose provider carries the default middleware.
func NewAgent(cfg ai.Config) (*ai.Agent, error) {
	p, err := NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	return ai.NewAgentWithProvider(p, cfg), nil
}
