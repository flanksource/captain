package cache

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/flanksource/commons/logger"
	_ "github.com/mattn/go-sqlite3"
	"github.com/samber/lo"
)

var (
	ErrCacheDisabled = errors.New("caching is disabled")
	ErrNotFound      = errors.New("cache entry not found")
)

type Config struct {
	DBPath  string
	TTL     time.Duration
	NoCache bool
}

type Entry struct {
	ID               int64
	CacheKey         string
	PromptHash       string
	Model            string
	Prompt           string
	Response         string
	Error            string
	TokensInput      int
	TokensOutput     int
	TokensReasoning  int
	TokensCacheRead  int
	TokensCacheWrite int
	TokensTotal      int
	CostUSD          float64
	DurationMS       int64
	Provider         string
	Temperature      float64
	MaxTokens        int
	CreatedAt        time.Time
	AccessedAt       time.Time
	ExpiresAt        *time.Time
}

type StatsEntry struct {
	Model                 string
	Provider              string
	TotalRequests         int64
	CacheHits             int64
	CacheMisses           int64
	ErrorCount            int64
	TotalInputTokens      int64
	TotalOutputTokens     int64
	TotalReasoningTokens  int64
	TotalCacheReadTokens  int64
	TotalCacheWriteTokens int64
	TotalCost             float64
	AvgDurationMS         int64
	FirstRequest          time.Time
	LastRequest           time.Time
}

type Cache struct {
	db     *sql.DB
	config Config
}

func (c Cache) GetTTL() time.Duration { return c.config.TTL }

func New(config Config) (*Cache, error) {
	if config.DBPath == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		config.DBPath = filepath.Join(homeDir, ".cache", "captain-llm.db")
	}

	if err := os.MkdirAll(filepath.Dir(config.DBPath), 0o750); err != nil {
		return nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	db, err := sql.Open("sqlite3", config.DBPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA cache_size = -64000",
		"PRAGMA busy_timeout = 5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to set pragma %s: %w", pragma, err)
		}
	}

	cache := &Cache{db: db, config: config}
	if _, err := db.Exec(embeddedSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	go cache.cleanupExpired()
	return cache, nil
}

func (c *Cache) Close() error { return c.db.Close() }

func generateCacheKey(prompt, model string) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s", prompt, model)))
	return fmt.Sprintf("%x", h)
}

func (c *Cache) Get(prompt, model string) (*Entry, error) {
	if c.config.NoCache {
		return nil, ErrCacheDisabled
	}

	cacheKey := generateCacheKey(prompt, model)

	var entry Entry
	var expiresAt sql.NullTime
	err := c.db.QueryRow(`
		SELECT id, cache_key, prompt_hash, model, prompt, response, error,
		       tokens_input, tokens_output, tokens_reasoning,
		       tokens_cache_read, tokens_cache_write, tokens_total,
		       cost_usd, duration_ms, provider, temperature, max_tokens,
		       created_at, accessed_at, expires_at
		FROM llm_cache
		WHERE cache_key = ?
		  AND (expires_at IS NULL OR expires_at > CURRENT_TIMESTAMP)
		ORDER BY created_at DESC
		LIMIT 1
	`, cacheKey).Scan(
		&entry.ID, &entry.CacheKey, &entry.PromptHash, &entry.Model,
		&entry.Prompt, &entry.Response, &entry.Error,
		&entry.TokensInput, &entry.TokensOutput, &entry.TokensReasoning,
		&entry.TokensCacheRead, &entry.TokensCacheWrite, &entry.TokensTotal,
		&entry.CostUSD, &entry.DurationMS, &entry.Provider,
		&entry.Temperature, &entry.MaxTokens,
		&entry.CreatedAt, &entry.AccessedAt, &expiresAt,
	)

	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to get cache entry: %w", err)
	}

	if expiresAt.Valid {
		entry.ExpiresAt = &expiresAt.Time
		if time.Now().After(expiresAt.Time) {
			return nil, ErrNotFound
		}
	}

	_, _ = c.db.Exec("UPDATE llm_cache SET accessed_at = CURRENT_TIMESTAMP WHERE id = ?", entry.ID)
	return &entry, nil
}

func (c *Cache) Set(entry *Entry) error {
	if c.config.NoCache {
		return nil
	}

	entry.CreatedAt = time.Now()
	entry.CacheKey = generateCacheKey(entry.Prompt, entry.Model)
	entry.PromptHash = entry.CacheKey

	logger.Tracef("[%s] caching response for %s (hash:%s)", entry.Model, lo.Ellipsis(entry.Prompt, 20), entry.PromptHash)

	var expiresAt *time.Time
	if c.config.TTL > 0 {
		exp := time.Now().Add(c.config.TTL)
		expiresAt = &exp
	}

	_, err := c.db.Exec(`
		INSERT OR REPLACE INTO llm_cache (
			cache_key, prompt_hash, model, prompt, response, error,
			tokens_input, tokens_output, tokens_reasoning,
			tokens_cache_read, tokens_cache_write, tokens_total,
			cost_usd, duration_ms, provider, temperature, max_tokens, expires_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		entry.CacheKey, entry.PromptHash, entry.Model, entry.Prompt, entry.Response, entry.Error,
		entry.TokensInput, entry.TokensOutput, entry.TokensReasoning,
		entry.TokensCacheRead, entry.TokensCacheWrite, entry.TokensTotal,
		entry.CostUSD, entry.DurationMS, entry.Provider, entry.Temperature, entry.MaxTokens,
		expiresAt,
	)
	if err != nil {
		return fmt.Errorf("failed to set cache entry: %w", err)
	}

	c.updateStats(entry)
	return nil
}

func (c *Cache) Clear() error {
	_, err := c.db.Exec("DELETE FROM llm_cache")
	return err
}

func (c *Cache) GetStats() ([]StatsEntry, error) {
	rows, err := c.db.Query(`
		SELECT model, provider,
		       COUNT(*) as total_requests,
		       SUM(CASE WHEN error IS NULL OR error = '' THEN 1 ELSE 0 END),
		       SUM(CASE WHEN error IS NOT NULL AND error != '' THEN 1 ELSE 0 END),
		       SUM(tokens_input), SUM(tokens_output), SUM(tokens_reasoning),
		       SUM(tokens_cache_read), SUM(tokens_cache_write),
		       SUM(cost_usd), AVG(duration_ms),
		       MIN(created_at), MAX(created_at)
		FROM llm_cache
		GROUP BY model, provider
		ORDER BY total_requests DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("failed to get stats: %w", err)
	}
	defer rows.Close()

	var stats []StatsEntry
	for rows.Next() {
		var s StatsEntry
		var provider sql.NullString
		var inputTok, outputTok, reasonTok, cacheReadTok, cacheWriteTok sql.NullInt64

		if err := rows.Scan(
			&s.Model, &provider,
			&s.TotalRequests, &s.CacheHits, &s.ErrorCount,
			&inputTok, &outputTok, &reasonTok,
			&cacheReadTok, &cacheWriteTok,
			&s.TotalCost, &s.AvgDurationMS,
			&s.FirstRequest, &s.LastRequest,
		); err != nil {
			return nil, fmt.Errorf("failed to scan stats: %w", err)
		}

		if provider.Valid {
			s.Provider = provider.String
		}
		if inputTok.Valid {
			s.TotalInputTokens = inputTok.Int64
		}
		if outputTok.Valid {
			s.TotalOutputTokens = outputTok.Int64
		}
		if reasonTok.Valid {
			s.TotalReasoningTokens = reasonTok.Int64
		}
		if cacheReadTok.Valid {
			s.TotalCacheReadTokens = cacheReadTok.Int64
		}
		if cacheWriteTok.Valid {
			s.TotalCacheWriteTokens = cacheWriteTok.Int64
		}

		stats = append(stats, s)
	}
	return stats, nil
}

func (c *Cache) cleanupExpired() {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		_, _ = c.db.Exec("DELETE FROM llm_cache WHERE expires_at IS NOT NULL AND expires_at < CURRENT_TIMESTAMP")
	}
}

func (c *Cache) updateStats(entry *Entry) {
	date := entry.CreatedAt.Format("2006-01-02")
	_, _ = c.db.Exec(`
		INSERT INTO llm_stats (
			date, model, provider, request_count,
			total_input_tokens, total_output_tokens, total_reasoning_tokens,
			total_cache_read_tokens, total_cache_write_tokens,
			total_cost_usd
		) VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date, model, provider) DO UPDATE SET
			request_count = request_count + 1,
			total_input_tokens = total_input_tokens + excluded.total_input_tokens,
			total_output_tokens = total_output_tokens + excluded.total_output_tokens,
			total_reasoning_tokens = total_reasoning_tokens + excluded.total_reasoning_tokens,
			total_cache_read_tokens = total_cache_read_tokens + excluded.total_cache_read_tokens,
			total_cache_write_tokens = total_cache_write_tokens + excluded.total_cache_write_tokens,
			total_cost_usd = total_cost_usd + excluded.total_cost_usd,
			updated_at = CURRENT_TIMESTAMP`,
		date, entry.Model, entry.Provider,
		entry.TokensInput, entry.TokensOutput, entry.TokensReasoning,
		entry.TokensCacheRead, entry.TokensCacheWrite,
		entry.CostUSD,
	)
}
