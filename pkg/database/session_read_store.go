package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SessionOverview is one row of the captain_session_overview view: the session
// identity/state plus its active process and usage/cost aggregates.
type SessionOverview struct {
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
	AgentType           *string         `gorm:"column:agent_type" json:"agentType,omitempty"`
	Description         *string         `gorm:"column:description" json:"description,omitempty"`
	CLIVersion          *string         `gorm:"column:cli_version" json:"cliVersion,omitempty"`
	LifecycleStatus     string          `gorm:"column:lifecycle_status" json:"lifecycleStatus"`
	ActivityState       string          `gorm:"column:activity_state" json:"activityState"`
	HealthState         string          `gorm:"column:health_state" json:"healthState"`
	Git                 json.RawMessage `gorm:"column:git" json:"git,omitempty"`
	Metadata            json.RawMessage `gorm:"column:metadata" json:"metadata,omitempty"`
	StartedAt           *time.Time      `gorm:"column:started_at" json:"startedAt,omitempty"`
	EndedAt             *time.Time      `gorm:"column:ended_at" json:"endedAt,omitempty"`
	LastActivityAt      *time.Time      `gorm:"column:last_activity_at" json:"lastActivityAt,omitempty"`
	DurationSeconds     *float64        `gorm:"column:duration_seconds" json:"durationSeconds,omitempty"`
	PID                 *int64          `gorm:"column:pid" json:"pid,omitempty"`
	ProcessStatus       *string         `gorm:"column:process_status" json:"processStatus,omitempty"`
	ProcessCommand      *string         `gorm:"column:command" json:"processCommand,omitempty"`
	ProcessCWD          *string         `gorm:"column:process_cwd" json:"processCwd,omitempty"`
	ProcessStartedAt    *time.Time      `gorm:"column:process_started_at" json:"processStartedAt,omitempty"`
	ProcessActive       bool            `gorm:"column:process_active" json:"processActive"`
	CPUPercent          *float64        `gorm:"column:cpu_percent" json:"cpuPercent,omitempty"`
	MemoryPercent       *float64        `gorm:"column:memory_percent" json:"memoryPercent,omitempty"`
	MemoryRSSBytes      *int64          `gorm:"column:memory_rss_bytes" json:"memoryRssBytes,omitempty"`
	MessageCount        int64           `gorm:"column:message_count" json:"messageCount"`
	ToolCallCount       int64           `gorm:"column:tool_call_count" json:"toolCallCount"`
	TurnCount           int64           `gorm:"column:turn_count" json:"turnCount"`
	AgentCount          int64           `gorm:"column:agent_count" json:"agentCount"`
	PromptRunCount      int64           `gorm:"column:prompt_run_count" json:"promptRunCount"`
	PlanCount           int64           `gorm:"column:plan_count" json:"planCount"`
	Model               *string         `gorm:"column:model" json:"model,omitempty"`
	Backend             *string         `gorm:"column:backend" json:"backend,omitempty"`
	Effort              *string         `gorm:"column:effort" json:"effort,omitempty"`
	ContextTokens       *int64          `gorm:"column:context_tokens" json:"contextTokens,omitempty"`
	ContextWindowTokens *int64          `gorm:"column:context_window_tokens" json:"contextWindowTokens,omitempty"`
	ContextFreePercent  *int            `gorm:"column:context_free_percent" json:"contextFreePercent,omitempty"`
	InputTokens         int64           `gorm:"column:input_tokens" json:"inputTokens"`
	OutputTokens        int64           `gorm:"column:output_tokens" json:"outputTokens"`
	ReasoningTokens     int64           `gorm:"column:reasoning_tokens" json:"reasoningTokens"`
	CacheReadTokens     int64           `gorm:"column:cache_read_tokens" json:"cacheReadTokens"`
	CacheWriteTokens    int64           `gorm:"column:cache_write_tokens" json:"cacheWriteTokens"`
	TotalTokens         int64           `gorm:"column:total_tokens" json:"totalTokens"`
	CostUSD             float64         `gorm:"column:cost_usd" json:"costUsd"`
}

func (SessionOverview) TableName() string { return "captain_session_overview" }

// SessionOverviewFilter narrows ListSessionOverviews. Zero values do not filter.
type SessionOverviewFilter struct {
	Source    string
	Project   string
	RootsOnly bool
	LiveOnly  bool
	Limit     int
}

func (db *DB) ListSessionOverviews(ctx context.Context, filter SessionOverviewFilter) ([]SessionOverview, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	query := db.gorm.WithContext(ctx).Model(&SessionOverview{}).
		Order("COALESCE(last_activity_at, started_at, created_at) DESC, id DESC")
	if source := strings.TrimSpace(filter.Source); source != "" {
		query = query.Where("source = ?", source)
	}
	if project := strings.TrimSpace(filter.Project); project != "" {
		query = query.Where("project = ?", project)
	}
	if filter.RootsOnly {
		query = query.Where("parent_session_id IS NULL")
	}
	if filter.LiveOnly {
		query = query.Where("process_active")
	}
	if filter.Limit > 0 {
		query = query.Limit(filter.Limit)
	}
	var rows []SessionOverview
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Captain session overviews: %w", err)
	}
	return rows, nil
}

// GetSessionOverviewByIdentity resolves a session UUID or a provider session ID
// prefix (the CLI accepts short IDs) to its overview row. Ambiguous prefixes
// are an error rather than an arbitrary pick.
func (db *DB) GetSessionOverviewByIdentity(ctx context.Context, identity string) (*SessionOverview, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return nil, fmt.Errorf("%w: identity is required", ErrInvalidSession)
	}
	if parsed, err := uuid.Parse(identity); err == nil {
		var row SessionOverview
		err := db.gorm.WithContext(ctx).First(&row, "id = ?", parsed).Error
		if err == nil {
			return &row, nil
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("resolve Captain session overview UUID: %w", err)
		}
	}
	var rows []SessionOverview
	err := db.gorm.WithContext(ctx).
		Where("provider_session_id LIKE ?", escapeLike(identity)+"%").
		Limit(3).Find(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("resolve Captain session overview identity: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, identity)
	}
	for i := range rows {
		if optionalString(rows[i].ProviderSessionID) == identity {
			return &rows[i], nil
		}
	}
	if len(rows) > 1 {
		return nil, fmt.Errorf("%w: session ID prefix %q is ambiguous", ErrSessionConflict, identity)
	}
	return &rows[0], nil
}

// TranscriptMessage is one row of the captain_session_transcript view.
type TranscriptMessage struct {
	ID                uuid.UUID       `gorm:"column:id" json:"id"`
	SessionID         uuid.UUID       `gorm:"column:session_id" json:"sessionId"`
	TurnID            *uuid.UUID      `gorm:"column:turn_id" json:"turnId,omitempty"`
	ProviderMessageID *string         `gorm:"column:provider_message_id" json:"providerMessageId,omitempty"`
	Sequence          int64           `gorm:"column:sequence" json:"sequence"`
	Role              string          `gorm:"column:role" json:"role"`
	Parts             json.RawMessage `gorm:"column:parts" json:"parts"`
	SourceLine        *int64          `gorm:"column:source_line" json:"sourceLine,omitempty"`
	OccurredAt        *time.Time      `gorm:"column:occurred_at" json:"occurredAt,omitempty"`
	Model             *string         `gorm:"column:model" json:"model,omitempty"`
}

func (TranscriptMessage) TableName() string { return "captain_session_transcript" }

// TranscriptPage selects a message window: Offset/Limit from the start, or the
// last Tail messages when Tail > 0 (Tail wins over Offset).
type TranscriptPage struct {
	SessionID uuid.UUID
	Offset    int
	Limit     int
	Tail      int
}

func (db *DB) ListTranscriptMessages(ctx context.Context, page TranscriptPage) ([]TranscriptMessage, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	if page.SessionID == uuid.Nil {
		return nil, fmt.Errorf("%w: session ID is required", ErrInvalidSession)
	}
	query := db.gorm.WithContext(ctx).Model(&TranscriptMessage{}).Where("session_id = ?", page.SessionID)
	var rows []TranscriptMessage
	if page.Tail > 0 {
		if err := query.Order("sequence DESC").Limit(page.Tail).Find(&rows).Error; err != nil {
			return nil, fmt.Errorf("tail Captain transcript messages: %w", err)
		}
		for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
			rows[left], rows[right] = rows[right], rows[left]
		}
		return rows, nil
	}
	query = query.Order("sequence ASC").Offset(page.Offset)
	if page.Limit > 0 {
		query = query.Limit(page.Limit)
	}
	if err := query.Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Captain transcript messages: %w", err)
	}
	return rows, nil
}

// ProjectAggregate summarizes one project's sessions for pickers/dashboards.
type ProjectAggregate struct {
	Project        string     `gorm:"column:project" json:"project"`
	SessionCount   int64      `gorm:"column:session_count" json:"sessionCount"`
	LiveCount      int64      `gorm:"column:live_count" json:"liveCount"`
	Sources        string     `gorm:"column:sources" json:"sources"`
	LastActivityAt *time.Time `gorm:"column:last_activity_at" json:"lastActivityAt,omitempty"`
}

func (db *DB) ListProjectAggregates(ctx context.Context) ([]ProjectAggregate, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	var rows []ProjectAggregate
	err := db.gorm.WithContext(ctx).Raw(`
		SELECT
		  s.project,
		  count(*) FILTER (WHERE s.parent_session_id IS NULL) AS session_count,
		  count(p.id) AS live_count,
		  string_agg(DISTINCT s.source, ',') AS sources,
		  max(s.last_activity_at) AS last_activity_at
		FROM captain_sessions s
		LEFT JOIN captain_session_processes p ON p.session_id = s.id AND p.ended_at IS NULL
		WHERE s.project IS NOT NULL AND s.project <> ''
		GROUP BY s.project
		ORDER BY max(s.last_activity_at) DESC NULLS LAST`).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list Captain project aggregates: %w", err)
	}
	return rows, nil
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
