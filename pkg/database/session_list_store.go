package database

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
)

const (
	defaultSessionListLimit = 100
	maxSessionListLimit     = 500
)

// SessionListSummary is the bounded list projection. Detail-only transcript,
// tool, event, plan, request, agent, and artifact aggregates intentionally do
// not belong to this type.
type SessionListSummary struct {
	ID                  uuid.UUID       `gorm:"column:id" json:"id"`
	ProviderSessionID   *string         `gorm:"column:provider_session_id" json:"providerSessionId,omitempty"`
	Source              string          `gorm:"column:source" json:"source"`
	Provider            string          `gorm:"column:provider" json:"provider,omitempty"`
	HostID              string          `gorm:"column:host_id" json:"hostId"`
	ParentSessionID     *uuid.UUID      `gorm:"column:parent_session_id" json:"parentSessionId,omitempty"`
	RootSessionID       *uuid.UUID      `gorm:"column:root_session_id" json:"rootSessionId,omitempty"`
	Path                *string         `gorm:"column:path" json:"path,omitempty"`
	HistoryFile         *string         `gorm:"column:history_file" json:"historyFile,omitempty"`
	Project             *string         `gorm:"column:project" json:"project,omitempty"`
	CWD                 *string         `gorm:"column:cwd" json:"cwd,omitempty"`
	Title               *string         `gorm:"column:title" json:"title,omitempty"`
	InitialPrompt       *string         `gorm:"column:initial_prompt" json:"initialPrompt,omitempty"`
	Slug                *string         `gorm:"column:slug" json:"slug,omitempty"`
	CLIVersion          *string         `gorm:"column:cli_version" json:"cliVersion,omitempty"`
	LifecycleStatus     string          `gorm:"column:lifecycle_status" json:"lifecycleStatus"`
	Git                 json.RawMessage `gorm:"column:git" json:"git,omitempty"`
	Metadata            json.RawMessage `gorm:"column:metadata" json:"metadata,omitempty"`
	StartedAt           *time.Time      `gorm:"column:started_at" json:"startedAt,omitempty"`
	EndedAt             *time.Time      `gorm:"column:ended_at" json:"endedAt,omitempty"`
	LastActivityAt      *time.Time      `gorm:"column:last_activity_at" json:"lastActivityAt,omitempty"`
	ActivityAt          time.Time       `gorm:"column:activity_at" json:"-"`
	PID                 *int64          `gorm:"column:pid" json:"pid,omitempty"`
	ProcessStatus       *string         `gorm:"column:process_status" json:"processStatus,omitempty"`
	ProcessCommand      *string         `gorm:"column:process_command" json:"processCommand,omitempty"`
	ProcessCWD          *string         `gorm:"column:process_cwd" json:"processCwd,omitempty"`
	ProcessStartedAt    *time.Time      `gorm:"column:process_started_at" json:"processStartedAt,omitempty"`
	ProcessSampledAt    *time.Time      `gorm:"column:process_sampled_at" json:"processSampledAt,omitempty"`
	LastHeartbeatAt     *time.Time      `gorm:"column:last_heartbeat_at" json:"lastHeartbeatAt,omitempty"`
	LeaseOwner          *string         `gorm:"column:lease_owner" json:"leaseOwner,omitempty"`
	LeaseExpiresAt      *time.Time      `gorm:"column:lease_expires_at" json:"leaseExpiresAt,omitempty"`
	ProcessActive       bool            `gorm:"column:process_active" json:"processActive"`
	CPUPercent          *float64        `gorm:"column:cpu_percent" json:"cpuPercent,omitempty"`
	MemoryPercent       *float64        `gorm:"column:memory_percent" json:"memoryPercent,omitempty"`
	Model               *string         `gorm:"column:model" json:"model,omitempty"`
	Backend             *string         `gorm:"column:backend" json:"backend,omitempty"`
	Effort              *string         `gorm:"column:effort" json:"effort,omitempty"`
	ContextTokens       *int64          `gorm:"column:context_tokens" json:"contextTokens,omitempty"`
	ContextWindowTokens *int64          `gorm:"column:context_window_tokens" json:"contextWindowTokens,omitempty"`
	ContextFreePercent  *int            `gorm:"column:context_free_percent" json:"contextFreePercent,omitempty"`
	InputTokens         int64           `gorm:"column:input_tokens" json:"inputTokens"`
	OutputTokens        int64           `gorm:"column:output_tokens" json:"outputTokens"`
	CacheReadTokens     int64           `gorm:"column:cache_read_tokens" json:"cacheReadTokens"`
	CacheWriteTokens    int64           `gorm:"column:cache_write_tokens" json:"cacheWriteTokens"`
	TotalTokens         int64           `gorm:"column:total_tokens" json:"totalTokens"`
	CostUSD             float64         `gorm:"column:cost_usd" json:"costUsd"`
	TotalCount          int64           `gorm:"column:total_count" json:"-"`
}

type SessionListFilter struct {
	Source      string
	Project     string
	ProjectRoot string
	Query       string
	RootsOnly   bool
	LiveOnly    bool
	Limit       int
	Cursor      string
}

type SessionListPage struct {
	Rows       []SessionListSummary `json:"rows"`
	Total      int64                `json:"total"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

type sessionListCursor struct {
	Version    int       `json:"v"`
	ActivityAt time.Time `json:"activityAt"`
	ID         uuid.UUID `json:"id"`
}

func (db *DB) ListSessionSummaries(ctx context.Context, filter SessionListFilter) (SessionListPage, error) {
	if err := db.requireGorm(); err != nil {
		return SessionListPage{}, err
	}
	limit, err := sessionListLimit(filter.Limit)
	if err != nil {
		return SessionListPage{}, err
	}
	cursor, err := decodeSessionListCursor(filter.Cursor)
	if err != nil {
		return SessionListPage{}, err
	}
	projectRoot := strings.TrimRight(strings.TrimSpace(filter.ProjectRoot), "/")
	args := []any{
		sql.Named("excluded_model", api.CodexAutoReviewModel),
		sql.Named("source", strings.TrimSpace(filter.Source)),
		sql.Named("project", strings.TrimSpace(filter.Project)),
		sql.Named("project_root", projectRoot),
		sql.Named("project_prefix", escapeLike(projectRoot)+"/%"),
		sql.Named("query", strings.TrimSpace(filter.Query)),
		sql.Named("roots_only", filter.RootsOnly),
		sql.Named("live_only", filter.LiveOnly),
		sql.Named("cursor_activity", cursorActivity(cursor)),
		sql.Named("cursor_id", cursorID(cursor)),
		sql.Named("fetch_limit", limit+1),
	}
	var rows []SessionListSummary
	if err := db.gorm.WithContext(ctx).Raw(sessionListQuery, args...).Scan(&rows).Error; err != nil {
		return SessionListPage{}, fmt.Errorf("list Captain session summaries: %w", err)
	}
	page := SessionListPage{Rows: rows}
	if len(rows) > 0 {
		page.Total = rows[0].TotalCount
	}
	if len(page.Rows) > limit {
		page.Rows = page.Rows[:limit]
		page.NextCursor, err = encodeSessionListCursor(page.Rows[limit-1])
		if err != nil {
			return SessionListPage{}, err
		}
	}
	return page, nil
}

func sessionListLimit(limit int) (int, error) {
	if limit == 0 {
		return defaultSessionListLimit, nil
	}
	if limit < 0 || limit > maxSessionListLimit {
		return 0, fmt.Errorf("session list limit must be between 1 and %d", maxSessionListLimit)
	}
	return limit, nil
}

func encodeSessionListCursor(row SessionListSummary) (string, error) {
	raw, err := json.Marshal(sessionListCursor{Version: 1, ActivityAt: row.ActivityAt, ID: row.ID})
	if err != nil {
		return "", fmt.Errorf("encode Captain session cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeSessionListCursor(value string) (*sessionListCursor, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("invalid Captain session cursor encoding: %w", err)
	}
	var cursor sessionListCursor
	if err := json.Unmarshal(raw, &cursor); err != nil {
		return nil, fmt.Errorf("invalid Captain session cursor payload: %w", err)
	}
	if cursor.Version != 1 || cursor.ActivityAt.IsZero() || cursor.ID == uuid.Nil {
		return nil, fmt.Errorf("invalid Captain session cursor values")
	}
	return &cursor, nil
}

func cursorActivity(cursor *sessionListCursor) any {
	if cursor == nil {
		return nil
	}
	return cursor.ActivityAt
}

func cursorID(cursor *sessionListCursor) any {
	if cursor == nil {
		return nil
	}
	return cursor.ID
}

const sessionListQuery = `
WITH latest_call AS MATERIALIZED (
  SELECT DISTINCT ON (t.session_id)
    t.session_id, c.model, c.backend, c.effort, c.context_tokens, c.context_window_tokens
  FROM captain_turns t
  JOIN captain_model_calls c ON c.turn_id = t.id
  ORDER BY t.session_id, COALESCE(c.ended_at, c.started_at, c.created_at) DESC, c.call_index DESC
), active_process AS MATERIALIZED (
  SELECT DISTINCT ON (p.session_id)
    p.session_id, p.pid, p.status, p.command, p.cwd, p.process_started_at, p.sampled_at,
    p.last_heartbeat_at, p.lease_owner, p.lease_expires_at, p.cpu_percent, p.memory_percent
  FROM captain_session_processes p
  WHERE p.ended_at IS NULL
  ORDER BY p.session_id, COALESCE(p.last_heartbeat_at, p.process_started_at) DESC, p.id DESC
), eligible AS MATERIALIZED (
  SELECT
    s.id, s.provider_session_id, s.source, s.provider, s.host_id, s.parent_session_id,
    s.root_session_id, s.path, s.project, s.cwd, s.title, s.initial_prompt, s.slug,
    s.cli_version, s.lifecycle_status, s.git, s.metadata, s.started_at, s.ended_at,
    s.last_activity_at, COALESCE(s.last_activity_at, s.started_at, s.created_at) AS activity_at,
    ap.pid, ap.status AS process_status, ap.command AS process_command, ap.cwd AS process_cwd,
    ap.process_started_at, ap.sampled_at AS process_sampled_at, ap.last_heartbeat_at,
    ap.lease_owner, ap.lease_expires_at, ap.session_id IS NOT NULL AS process_active,
    ap.cpu_percent, ap.memory_percent, lc.model, lc.backend, lc.effort,
    lc.context_tokens, lc.context_window_tokens, count(*) OVER () AS total_count
  FROM captain_sessions s
  LEFT JOIN latest_call lc ON lc.session_id = s.id
  LEFT JOIN active_process ap ON ap.session_id = s.id
  WHERE COALESCE(s.metadata->>'model', lc.model, '') <> @excluded_model
    AND (@source = '' OR s.source = @source)
    AND (@project = '' OR s.project = @project)
    AND (@project_root = '' OR s.cwd = @project_root OR s.cwd LIKE @project_prefix ESCAPE '\')
    AND (NOT @roots_only OR s.parent_session_id IS NULL)
    AND (NOT @live_only OR ap.session_id IS NOT NULL)
    AND (
      @query = '' OR concat_ws(' ', s.id::text, s.provider_session_id, s.source, s.provider,
        s.project, s.cwd, s.title, s.initial_prompt, s.slug, s.cli_version,
        s.git->>'branch', lc.model, lc.backend, lc.effort, ap.pid::text, ap.status,
        ap.cwd, ap.command) ILIKE '%' || @query || '%'
    )
), paged AS MATERIALIZED (
  SELECT *
  FROM eligible
  WHERE CAST(@cursor_activity AS timestamptz) IS NULL OR
    (activity_at, id) < (CAST(@cursor_activity AS timestamptz), CAST(@cursor_id AS uuid))
  ORDER BY activity_at DESC, id DESC
  LIMIT @fetch_limit
), latest_source AS (
  SELECT DISTINCT ON (src.session_id) src.session_id, src.path
  FROM captain_session_sources src
  JOIN paged p ON p.id = src.session_id
  ORDER BY src.session_id, src.updated_at DESC, src.id DESC
), call_stats AS (
  SELECT
    t.session_id,
    COALESCE(sum(c.input_tokens), 0)::bigint AS input_tokens,
    COALESCE(sum(c.output_tokens), 0)::bigint AS output_tokens,
    COALESCE(sum(c.cache_read_tokens), 0)::bigint AS cache_read_tokens,
    COALESCE(sum(c.cache_write_tokens), 0)::bigint AS cache_write_tokens,
    COALESCE(sum(c.input_tokens + c.output_tokens + c.cache_read_tokens + c.cache_write_tokens), 0)::bigint AS total_tokens,
    COALESCE(sum(c.input_cost + c.output_cost + c.reasoning_cost + c.cache_read_cost + c.cache_write_cost)
      FILTER (WHERE upper(c.currency) = 'USD'), 0::numeric) AS cost_usd
  FROM captain_turns t
  JOIN paged p ON p.id = t.session_id
  JOIN captain_model_calls c ON c.turn_id = t.id
  GROUP BY t.session_id
)
SELECT
  p.id, p.provider_session_id, p.source, p.provider, p.host_id, p.parent_session_id,
  p.root_session_id, p.path, COALESCE(src.path, p.path) AS history_file, p.project, p.cwd,
  p.title, p.initial_prompt, p.slug, p.cli_version, p.lifecycle_status, p.git, p.metadata,
  p.started_at, p.ended_at, p.last_activity_at, p.activity_at, p.pid, p.process_status,
  p.process_command, p.process_cwd, p.process_started_at, p.process_sampled_at,
  p.last_heartbeat_at, p.lease_owner, p.lease_expires_at, p.process_active,
  p.cpu_percent, p.memory_percent, p.model, p.backend, p.effort, p.context_tokens,
  p.context_window_tokens,
  CASE WHEN p.context_window_tokens > 0 THEN GREATEST(0, LEAST(100,
    round((1 - p.context_tokens::numeric / p.context_window_tokens::numeric) * 100)::integer)) END AS context_free_percent,
  COALESCE(cs.input_tokens, 0) AS input_tokens,
  COALESCE(cs.output_tokens, 0) AS output_tokens,
  COALESCE(cs.cache_read_tokens, 0) AS cache_read_tokens,
  COALESCE(cs.cache_write_tokens, 0) AS cache_write_tokens,
  COALESCE(cs.total_tokens, 0) AS total_tokens,
  COALESCE(cs.cost_usd, 0::numeric) AS cost_usd,
  p.total_count
FROM paged p
LEFT JOIN latest_source src ON src.session_id = p.id
LEFT JOIN call_stats cs ON cs.session_id = p.id
ORDER BY p.activity_at DESC, p.id DESC`
