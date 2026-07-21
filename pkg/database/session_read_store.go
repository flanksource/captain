package database

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
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
	ProcessSampledAt    *time.Time      `gorm:"column:process_sampled_at" json:"processSampledAt,omitempty"`
	LastHeartbeatAt     *time.Time      `gorm:"column:last_heartbeat_at" json:"lastHeartbeatAt,omitempty"`
	LeaseOwner          *string         `gorm:"column:lease_owner" json:"leaseOwner,omitempty"`
	LeaseExpiresAt      *time.Time      `gorm:"column:lease_expires_at" json:"leaseExpiresAt,omitempty"`
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

// SessionIdentityMatch is a lightweight captain_sessions row used for fast
// UUID/provider-prefix resolution without evaluating the aggregate overview.
type SessionIdentityMatch struct {
	ID                uuid.UUID  `gorm:"column:id"`
	ProviderSessionID string     `gorm:"column:provider_session_id"`
	Source            string     `gorm:"column:source"`
	HostID            string     `gorm:"column:host_id"`
	Path              string     `gorm:"column:path"`
	Project           string     `gorm:"column:project"`
	LastActivityAt    *time.Time `gorm:"column:last_activity_at"`
}

// SessionConflictError identifies every database session matching an
// ambiguous user-supplied prefix so callers can retry with a Captain UUID.
type SessionConflictError struct {
	Identity string
	Matches  []SessionIdentityMatch
}

func (e *SessionConflictError) Error() string {
	var message strings.Builder
	fmt.Fprintf(&message, "%s: session ID prefix %q matches %d sessions", ErrSessionConflict, e.Identity, len(e.Matches))
	for _, match := range e.Matches {
		fmt.Fprintf(&message, "\n  %s  %s", match.ID, match.Source)
		if match.ProviderSessionID != "" {
			fmt.Fprintf(&message, "  provider=%s", match.ProviderSessionID)
		}
		if match.Project != "" {
			fmt.Fprintf(&message, "  project=%s", match.Project)
		}
		if match.HostID != "" {
			fmt.Fprintf(&message, "  host=%s", match.HostID)
		}
		if match.Path != "" {
			fmt.Fprintf(&message, "  path=%s", match.Path)
		}
	}
	message.WriteString("\nRetry with a full Captain UUID from the first column.")
	return message.String()
}

func (e *SessionConflictError) Unwrap() error { return ErrSessionConflict }

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
		Where("COALESCE(metadata->>'model', model, '') <> ?", api.CodexAutoReviewModel).
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

// CountLiveRootSessions returns the number of top-level sessions currently
// associated with an active process. It uses the same overview projection and
// root/live semantics as the session list surfaces.
func (db *DB) CountLiveRootSessions(ctx context.Context) (int64, error) {
	if err := db.requireGorm(); err != nil {
		return 0, err
	}
	var count int64
	if err := db.gorm.WithContext(ctx).Model(&SessionOverview{}).
		Where("COALESCE(metadata->>'model', model, '') <> ?", api.CodexAutoReviewModel).
		Where("parent_session_id IS NULL").
		Where("process_active").
		Count(&count).Error; err != nil {
		return 0, fmt.Errorf("count live Captain sessions: %w", err)
	}
	return count, nil
}

// GetSessionOverviewByIdentity resolves a full Captain UUID or a provider
// session ID prefix to its overview row. Ambiguous prefixes list every match.
func (db *DB) GetSessionOverviewByIdentity(ctx context.Context, identity string) (*SessionOverview, error) {
	rows, err := db.ListSessionOverviewsByIdentity(ctx, identity)
	if err != nil {
		return nil, err
	}
	if len(rows) > 1 {
		matches, matchErr := db.ListSessionIdentityMatches(ctx, identity)
		if matchErr != nil {
			return nil, matchErr
		}
		return nil, &SessionConflictError{Identity: strings.TrimSpace(identity), Matches: matches}
	}
	return &rows[0], nil
}

// ListSessionOverviewsByIdentity resolves a full Captain UUID or every session
// whose provider session ID starts with identity. It narrows the aggregate view
// by Captain UUIDs discovered from the lightweight base table.
func (db *DB) ListSessionOverviewsByIdentity(ctx context.Context, identity string) ([]SessionOverview, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return nil, fmt.Errorf("%w: identity is required", ErrInvalidSession)
	}
	if parsed, err := uuid.Parse(identity); err == nil {
		row, getErr := db.getSessionOverviewByID(ctx, parsed)
		if getErr == nil {
			return []SessionOverview{*row}, nil
		}
		if !errors.Is(getErr, gorm.ErrRecordNotFound) {
			return nil, getErr
		}
	}
	matches, err := db.ListSessionIdentityMatches(ctx, identity)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, identity)
	}
	ids := make([]uuid.UUID, len(matches))
	for i := range matches {
		ids[i] = matches[i].ID
	}
	var rows []SessionOverview
	if err := db.gorm.WithContext(ctx).Where("id IN ?", ids).Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Captain session overviews by identity: %w", err)
	}
	byID := make(map[uuid.UUID]SessionOverview, len(rows))
	for i := range rows {
		byID[rows[i].ID] = rows[i]
	}
	ordered := make([]SessionOverview, 0, len(matches))
	for _, match := range matches {
		row, ok := byID[match.ID]
		if !ok {
			return nil, fmt.Errorf("%w: overview missing for matched Captain session %s", ErrInvalidSession, match.ID)
		}
		ordered = append(ordered, row)
	}
	return ordered, nil
}

// ListSessionOverviewsByProviderSessionID returns every Captain session bound
// to the complete provider session ID. Provider identities may legitimately be
// shared across sources, providers, and hosts, so this lookup is plural.
func (db *DB) ListSessionOverviewsByProviderSessionID(ctx context.Context, providerSessionID string) ([]SessionOverview, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	providerSessionID = strings.TrimSpace(providerSessionID)
	if providerSessionID == "" {
		return nil, fmt.Errorf("%w: provider session ID is required", ErrInvalidSession)
	}
	var rows []SessionOverview
	if err := db.gorm.WithContext(ctx).
		Where("provider_session_id = ?", providerSessionID).
		Order("COALESCE(last_activity_at, started_at, created_at) DESC, id").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Captain sessions by provider session ID: %w", err)
	}
	return rows, nil
}

// ListSessionIdentityMatches resolves prefixes against the lightweight base
// table so ambiguous lookups do not evaluate every overview aggregate.
func (db *DB) ListSessionIdentityMatches(ctx context.Context, identity string) ([]SessionIdentityMatch, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return nil, fmt.Errorf("%w: identity is required", ErrInvalidSession)
	}
	prefix := escapeLike(identity) + "%"
	var matches []SessionIdentityMatch
	err := db.gorm.WithContext(ctx).
		Table("captain_sessions").
		Where("provider_session_id LIKE ?", prefix).
		Order("provider_session_id, (path IS NULL), last_activity_at DESC NULLS LAST, id").
		Find(&matches).Error
	if err != nil {
		return nil, fmt.Errorf("list Captain session identity matches: %w", err)
	}
	return matches, nil
}

func (db *DB) getSessionOverviewByID(ctx context.Context, id uuid.UUID) (*SessionOverview, error) {
	var row SessionOverview
	if err := db.gorm.WithContext(ctx).First(&row, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		return nil, fmt.Errorf("resolve Captain session overview UUID: %w", err)
	}
	return &row, nil
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
		  AND COALESCE(s.metadata->>'model', '') <> ?
		GROUP BY s.project
		ORDER BY max(s.last_activity_at) DESC NULLS LAST`, api.CodexAutoReviewModel).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list Captain project aggregates: %w", err)
	}
	return rows, nil
}

func escapeLike(value string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(value)
}
