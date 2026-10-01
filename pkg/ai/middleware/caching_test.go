package middleware

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/cache"
	"github.com/flanksource/captain/pkg/api"
)

func TestCachingProvider_ServesSecondCallFromCache(t *testing.T) {
	const answer = "cached answer"
	c, err := cache.New(cache.Config{DBPath: filepath.Join(t.TempDir(), "cache.db"), TTL: time.Hour})
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("cache.Close: %v", err)
		}
	})
	inner := &scriptedProvider{responses: []string{answer}}
	p, err := Wrap(inner, WithCacheInstance(c))
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	req := ai.Request{Prompt: api.Prompt{User: "what is cached?"}}

	first, err := p.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	second, err := p.Execute(context.Background(), req)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}

	if inner.calls != 1 {
		t.Errorf("provider calls = %d, want 1 (second call served from cache)", inner.calls)
	}
	if first.CacheHit || !second.CacheHit {
		t.Errorf("CacheHit = (%v, %v), want (false, true)", first.CacheHit, second.CacheHit)
	}
	if second.Text != answer {
		t.Errorf("cached Text = %q, want %q", second.Text, answer)
	}
}
