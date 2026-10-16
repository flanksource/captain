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

// SessionListSummary is the bounded list projection of one
// captain_session_overview_rows row.
type SessionListSummary struct {
	ID                  uuid.UUID             `gorm:"column:id" json:"id"`
	ProviderSessionID   *string               `gorm:"column:provider_session_id" json:"providerSessionId,omitempty"`
	Source              string                `gorm:"column:source" json:"source"`
	Provider            string                `gorm:"column:provider" json:"provider,omitempty"`
	HostID              string                `gorm:"column:host_id" json:"hostId"`
	ParentSessionID     *uuid.UUID            `gorm:"column:parent_session_id" json:"parentSessionId,omitempty"`
	ParentRelation      SessionParentRelation `gorm:"column:parent_relation" json:"parentRelation,omitempty"`
	RootSessionID       *uuid.UUID            `gorm:"column:root_session_id" json:"rootSessionId,omitempty"`
	Path                *string               `gorm:"column:path" json:"path,omitempty"`
	HistoryFile         *string               `gorm:"column:history_file" json:"historyFile,omitempty"`
	Project             *string               `gorm:"column:project" json:"project,omitempty"`
	CWD                 *string               `gorm:"column:cwd" json:"cwd,omitempty"`
	Title               *string               `gorm:"column:title" json:"title,omitempty"`
	InitialPrompt       *string               `gorm:"column:initial_prompt" json:"initialPrompt,omitempty"`
	Slug                *string               `gorm:"column:slug" json:"slug,omitempty"`
	CLIVersion          *string               `gorm:"column:cli_version" json:"cliVersion,omitempty"`
	LifecycleStatus     string                `gorm:"column:lifecycle_status" json:"lifecycleStatus"`
	ActivityState       string                `gorm:"column:activity_state" json:"activityState"`
	StateReason         *string               `gorm:"column:state_reason" json:"stateReason,omitempty"`
	Git                 json.RawMessage       `gorm:"column:git" json:"git,omitempty"`
	Metadata            json.RawMessage       `gorm:"column:metadata" json:"metadata,omitempty"`
	StartedAt           *time.Time            `gorm:"column:started_at" json:"startedAt,omitempty"`
	EndedAt             *time.Time            `gorm:"column:ended_at" json:"endedAt,omitempty"`
	LastActivityAt      *time.Time            `gorm:"column:last_activity_at" json:"lastActivityAt,omitempty"`
	ActivityAt          time.Time             `gorm:"column:activity_at" json:"-"`
	PID                 *int64                `gorm:"column:pid" json:"pid,omitempty"`
	ProcessStatus       *string               `gorm:"column:process_status" json:"processStatus,omitempty"`
	ProcessCommand      *string               `gorm:"column:process_command" json:"processCommand,omitempty"`
	ProcessCWD          *string               `gorm:"column:process_cwd" json:"processCwd,omitempty"`
	ProcessStartedAt    *time.Time            `gorm:"column:process_started_at" json:"processStartedAt,omitempty"`
	ProcessSampledAt    *time.Time            `gorm:"column:process_sampled_at" json:"processSampledAt,omitempty"`
	LastHeartbeatAt     *time.Time            `gorm:"column:last_heartbeat_at" json:"lastHeartbeatAt,omitempty"`
	LeaseOwner          *string               `gorm:"column:lease_owner" json:"leaseOwner,omitempty"`
	LeaseExpiresAt      *time.Time            `gorm:"column:lease_expires_at" json:"leaseExpiresAt,omitempty"`
	ProcessActive       bool                  `gorm:"column:process_active" json:"processActive"`
	CPUPercent          *float64              `gorm:"column:cpu_percent" json:"cpuPercent,omitempty"`
	MemoryPercent       *float64              `gorm:"column:memory_percent" json:"memoryPercent,omitempty"`
	Model               *string               `gorm:"column:model" json:"model,omitempty"`
	ModelProvider       *string               `gorm:"column:model_provider" json:"modelProvider,omitempty"`
	ModelMode           *string               `gorm:"column:model_mode" json:"modelMode,omitempty"`
	Effort              *string               `gorm:"column:effort" json:"effort,omitempty"`
	ContextTokens       *int64                `gorm:"column:context_tokens" json:"contextTokens,omitempty"`
	ContextWindowTokens *int64                `gorm:"column:context_window_tokens" json:"contextWindowTokens,omitempty"`
	ContextFreePercent  *int                  `gorm:"column:context_free_percent" json:"contextFreePercent,omitempty"`
	InputTokens         int64                 `gorm:"column:input_tokens" json:"inputTokens"`
	OutputTokens        int64                 `gorm:"column:output_tokens" json:"outputTokens"`
	CacheReadTokens     int64                 `gorm:"column:cache_read_tokens" json:"cacheReadTokens"`
	CacheWriteTokens    int64                 `gorm:"column:cache_write_tokens" json:"cacheWriteTokens"`
	TotalTokens         int64                 `gorm:"column:total_tokens" json:"totalTokens"`
	CostUSD             float64               `gorm:"column:cost_usd" json:"costUsd"`
	// ProviderCostUSD is how much of CostUSD the providers reported themselves.
	// CostUSD falls back to list-priced buckets per call, so zero here means the
	// total is a reconstruction and must be rendered as an estimate.
	ProviderCostUSD float64 `gorm:"column:provider_cost_usd" json:"providerCostUsd,omitempty"`
	MessageCount    int64   `gorm:"column:message_count" json:"messageCount"`
	ToolCallCount   int64   `gorm:"column:tool_call_count" json:"toolCallCount"`
	TotalCount      int64   `gorm:"column:total_count" json:"-"`
}

type SessionListFilter struct {
	Source      string
	Project     string
	ProjectRoot string
	Query       string
	RootsOnly   bool
	// ExcludeSubagents drops "agent" children (subagents) and keeps roots and
	// "transcript" children — the agent session a run binds under its
	// admission row.
	ExcludeSubagents     bool
	ExcludeInactiveEmpty bool
	LiveOnly             bool
	ActivityFrom         *time.Time
	ActivityBefore       *time.Time
	Limit                int
	Cursor               string
	// IncludeTotal counts every session the filters match, ignoring Cursor
	// and Limit. Counting reads all matches instead of stopping at one page,
	// so callers that never show a total leave it off and get Total 0.
	IncludeTotal bool
}

type SessionListPage struct {
	Rows []SessionListSummary `json:"rows"`
	// Total is set only when the filter asked for it (IncludeTotal).
	Total      int64  `json:"total"`
	NextCursor string `json:"nextCursor,omitempty"`
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
	var rows []SessionListSummary
	if err := db.gorm.WithContext(ctx).Raw(sessionListQuery(filter.IncludeTotal), sessionListArgs(filter, cursor, limit)...).Scan(&rows).Error; err != nil {
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

func sessionListArgs(filter SessionListFilter, cursor *sessionListCursor, limit int) []any {
	projectRoot := strings.TrimRight(strings.TrimSpace(filter.ProjectRoot), "/")
	return []any{
		sql.Named("excluded_model", api.CodexAutoReviewModel),
		sql.Named("source", strings.TrimSpace(filter.Source)),
		sql.Named("project", strings.TrimSpace(filter.Project)),
		sql.Named("project_root", projectRoot),
		sql.Named("project_prefix", escapeLike(projectRoot)+"/%"),
		sql.Named("query", strings.TrimSpace(filter.Query)),
		sql.Named("roots_only", filter.RootsOnly),
		sql.Named("exclude_subagents", filter.ExcludeSubagents),
		sql.Named("exclude_inactive_empty", filter.ExcludeInactiveEmpty),
		sql.Named("live_only", filter.LiveOnly),
		sql.Named("activity_from", filter.ActivityFrom),
		sql.Named("activity_before", filter.ActivityBefore),
		sql.Named("cursor_activity", cursorActivity(cursor)),
		sql.Named("cursor_id", cursorID(cursor)),
		sql.Named("fetch_limit", limit+1),
	}
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

// sessionListQuery reads one page from captain_session_overview_rows, which
// migration 88 keeps current, keyset-paged on (activity_at, id) so a page is
// a bounded walk of captain_session_overview_rows_activity_idx. The total,
// when asked for, is an uncorrelated count of every match that ignores the
// cursor; it runs once and leaves the page on the index.
func sessionListQuery(includeTotal bool) string {
	total := "0::bigint"
	if includeTotal {
		total = "(SELECT count(*) " + sessionListMatches + ")"
	}
	return "SELECT " + sessionListColumns + ", " + total + " AS total_count\n" +
		sessionListMatches + "\n  AND " + sessionListAfterCursor +
		"\nORDER BY o.activity_at DESC, o.id DESC\nLIMIT @fetch_limit"
}

const sessionListColumns = `
  o.id, o.provider_session_id, o.source, o.provider, o.host_id, o.parent_session_id, o.parent_relation,
  o.root_session_id, o.path, o.history_file, o.project, o.cwd, o.title, o.initial_prompt, o.slug,
  o.cli_version, o.lifecycle_status, o.activity_state, o.state_reason, o.git, o.metadata,
  o.started_at, o.ended_at, o.last_activity_at, o.activity_at, o.pid, o.process_status,
  o.command AS process_command, o.process_cwd, o.process_started_at, o.process_sampled_at,
  o.last_heartbeat_at, o.lease_owner, o.lease_expires_at, o.process_active,
  o.cpu_percent, o.memory_percent, o.model, o.model_provider, o.model_mode, o.effort,
  o.context_tokens, o.context_window_tokens, o.context_free_percent,
  o.input_tokens, o.output_tokens, o.cache_read_tokens, o.cache_write_tokens, o.total_tokens,
  o.cost_usd, o.provider_cost_usd, o.message_count, o.tool_call_count`

const sessionListMatches = `FROM captain_session_overview_rows o
WHERE COALESCE(o.metadata->>'model', o.model, '') <> @excluded_model
  AND (@source = '' OR o.source = @source)
  AND (@project = '' OR o.project = @project)
  AND (@project_root = '' OR o.cwd = @project_root OR o.cwd LIKE @project_prefix ESCAPE '\')
  AND (NOT @roots_only OR o.parent_session_id IS NULL)
  AND (NOT @exclude_subagents OR o.parent_relation IS DISTINCT FROM 'agent')
  AND (NOT @live_only OR o.process_active)
  AND (NOT @exclude_inactive_empty OR
    o.process_active OR o.activity_state IN ('ask', 'approval') OR
    NULLIF(btrim(o.path), '') IS NOT NULL OR NULLIF(btrim(o.initial_prompt), '') IS NOT NULL OR
    o.source_path_present OR o.message_count > 0 OR o.prompt_run_count > 0 OR o.execution_run_count > 0)
  AND (CAST(@activity_from AS timestamptz) IS NULL OR o.activity_at >= CAST(@activity_from AS timestamptz))
  AND (CAST(@activity_before AS timestamptz) IS NULL OR o.activity_at < CAST(@activity_before AS timestamptz))
  AND (@query = '' OR o.search_text ILIKE '%' || @query || '%')`

const sessionListAfterCursor = `(CAST(@cursor_activity AS timestamptz) IS NULL OR
    (o.activity_at, o.id) < (CAST(@cursor_activity AS timestamptz), CAST(@cursor_id AS uuid)))`
