package pricing

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"
)

// log is the package-scoped logger for AI providers. Its level follows
// -v/--log-level and can be tuned with -Plog.level.ai=debug.
var log = logger.GetLogger("ai")

const (
	openRouterAPIURL    = "https://openrouter.ai/api/v1/models"
	cacheExpiryDuration = 24 * time.Hour
)

var (
	pricingCache     *PricingCache
	pricingCacheErr  error
	pricingCacheLock sync.Mutex
)

type OpenRouterResponse struct {
	Data []OpenRouterModel `json:"data"`
}

type OpenRouterModel struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Pricing       OpenRouterPricing `json:"pricing"`
	ContextLength int               `json:"context_length"`
	TopProvider   *TopProvider      `json:"top_provider,omitempty"`
}

type OpenRouterPricing struct {
	Prompt          string `json:"prompt"`
	Completion      string `json:"completion"`
	InputCacheRead  string `json:"input_cache_read,omitempty"`
	InputCacheWrite string `json:"input_cache_write,omitempty"`
}

type TopProvider struct {
	MaxCompletionTokens *int `json:"max_completion_tokens,omitempty"`
}

type PricingCache struct {
	Timestamp time.Time             `json:"timestamp"`
	Models    map[string]*ModelInfo `json:"models"`
}

func (c *PricingCache) IsExpired() bool {
	return time.Since(c.Timestamp) >= cacheExpiryDuration
}

// LoadOptions controls how EnsureLoaded resolves the pricing snapshot.
type LoadOptions struct {
	// Refresh skips both the in-memory and the on-disk snapshot and re-queries
	// OpenRouter, so `captain whoami --no-cache` reports live prices. A failed
	// refresh keeps the snapshot already installed instead of emptying pricing.
	Refresh bool
}

// EnsureLoaded installs the OpenRouter pricing snapshot, fetching it only when
// no usable one is already in memory or on disk (or when opts.Refresh forces it).
func EnsureLoaded(opts LoadOptions) {
	pricingCacheLock.Lock()
	defer pricingCacheLock.Unlock()

	if !opts.Refresh {
		if pricingCacheErr != nil {
			return
		}
		if installedPricingIsFresh() {
			return
		}
	}

	models, err := fetchPricing()
	if err != nil {
		log.Warnf("Failed to fetch OpenRouter pricing: %v", err)
		pricingCacheErr = err
		return
	}

	pricingCache = &PricingCache{Timestamp: time.Now(), Models: models}
	pricingCacheErr = nil
	MergeModels(models)
	applyCatalogPrices()
}

// installedPricingIsFresh reports whether an unexpired snapshot is in play,
// promoting the on-disk one into memory (and into the registry) when it is the
// only copy. Callers must hold pricingCacheLock.
func installedPricingIsFresh() bool {
	if pricingCache != nil {
		return !pricingCache.IsExpired()
	}
	cache, err := loadFromDisk()
	if err != nil || cache == nil || cache.IsExpired() {
		return false
	}
	log.Debugf("Loaded OpenRouter pricing from cache (age: %s)", time.Since(cache.Timestamp))
	pricingCache = cache
	MergeModels(cache.Models)
	applyCatalogPrices()
	return true
}

// fetchPricing is the live OpenRouter query, a package var so tests can
// substitute deterministic rows without hitting the network.
var fetchPricing = fetchOpenRouterPricing

func fetchOpenRouterPricing() (map[string]*ModelInfo, error) {
	resp, err := http.Get(openRouterAPIURL)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch OpenRouter pricing: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenRouter API returned status %d", resp.StatusCode)
	}

	var apiResp OpenRouterResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("failed to decode OpenRouter response: %w", err)
	}

	models := parseModels(apiResp.Data)

	cache := &PricingCache{Timestamp: time.Now(), Models: models}
	if err := saveToDisk(cache); err != nil {
		log.Warnf("Failed to save OpenRouter pricing cache: %v", err)
	}

	return models, nil
}

func parseModels(models []OpenRouterModel) map[string]*ModelInfo {
	result := make(map[string]*ModelInfo, len(models))
	for _, m := range models {
		info := &ModelInfo{
			ModelID:       m.ID,
			ContextWindow: m.ContextLength,
		}
		if p := parsePrice(m.Pricing.Prompt); p > 0 {
			info.InputPrice = p * 1_000_000
		}
		if p := parsePrice(m.Pricing.Completion); p > 0 {
			info.OutputPrice = p * 1_000_000
		}
		if p := parsePrice(m.Pricing.InputCacheRead); p > 0 {
			info.CacheReadsPrice = p * 1_000_000
		}
		if p := parsePrice(m.Pricing.InputCacheWrite); p > 0 {
			info.CacheWritesPrice = p * 1_000_000
		}
		if m.TopProvider != nil && m.TopProvider.MaxCompletionTokens != nil {
			info.MaxTokens = *m.TopProvider.MaxCompletionTokens
		}
		result[m.ID] = info
	}
	return result
}

func parsePrice(s string) float64 {
	if s == "" {
		return 0
	}
	var p float64
	_, _ = fmt.Sscanf(s, "%f", &p)
	return p
}

func getCachePath() (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(cacheDir, "flanksource")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return filepath.Join(dir, "openrouter-pricing.json"), nil
}

func loadFromDisk() (*PricingCache, error) {
	path, err := getCachePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var cache PricingCache
	if err := json.Unmarshal(data, &cache); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return &cache, nil
}

func saveToDisk(cache *PricingCache) error {
	path, err := getCachePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
