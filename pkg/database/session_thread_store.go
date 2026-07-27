package database

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// SessionTurn is the complete per-turn accounting projection used by threaded
// session viewers.
type SessionTurn struct {
	ID               uuid.UUID  `gorm:"column:id" json:"id"`
	SessionID        uuid.UUID  `gorm:"column:session_id" json:"sessionId"`
	ProviderTurnID   *string    `gorm:"column:provider_turn_id" json:"providerTurnId,omitempty"`
	TurnIndex        int        `gorm:"column:turn_index" json:"turnIndex"`
	Status           string     `gorm:"column:status" json:"status"`
	StopReason       *string    `gorm:"column:stop_reason" json:"stopReason,omitempty"`
	Error            *string    `gorm:"column:error" json:"error,omitempty"`
	StartedAt        *time.Time `gorm:"column:started_at" json:"startedAt,omitempty"`
	EndedAt          *time.Time `gorm:"column:ended_at" json:"endedAt,omitempty"`
	Model            *string    `gorm:"column:model" json:"model,omitempty"`
	Backend          *string    `gorm:"column:backend" json:"backend,omitempty"`
	Effort           *string    `gorm:"column:effort" json:"effort,omitempty"`
	ModelCallCount   int64      `gorm:"column:model_call_count" json:"modelCallCount"`
	InputTokens      int64      `gorm:"column:input_tokens" json:"inputTokens"`
	OutputTokens     int64      `gorm:"column:output_tokens" json:"outputTokens"`
	ReasoningTokens  int64      `gorm:"column:reasoning_tokens" json:"reasoningTokens"`
	CacheReadTokens  int64      `gorm:"column:cache_read_tokens" json:"cacheReadTokens"`
	CacheWriteTokens int64      `gorm:"column:cache_write_tokens" json:"cacheWriteTokens"`
	TotalTokens      int64      `gorm:"column:total_tokens" json:"totalTokens"`
	CostUSD          float64    `gorm:"column:cost_usd" json:"costUsd"`
	MessageCount     int64      `gorm:"column:message_count" json:"messageCount"`
}

func (SessionTurn) TableName() string { return "captain_session_turns" }

// SessionAgent is one root or child session in a provider thread.
type SessionAgent struct {
	ID               uuid.UUID  `gorm:"column:id" json:"id"`
	SessionID        uuid.UUID  `gorm:"column:session_id" json:"sessionId"`
	ParentSessionID  *uuid.UUID `gorm:"column:parent_session_id" json:"parentSessionId,omitempty"`
	RootSessionID    *uuid.UUID `gorm:"column:root_session_id" json:"rootSessionId,omitempty"`
	IsRoot           bool       `gorm:"column:is_root" json:"isRoot"`
	AgentType        *string    `gorm:"column:agent_type" json:"agentType,omitempty"`
	Description      *string    `gorm:"column:description" json:"description,omitempty"`
	HistoryFile      *string    `gorm:"column:history_file" json:"historyFile,omitempty"`
	Source           string     `gorm:"column:source" json:"source"`
	Provider         string     `gorm:"column:provider" json:"provider,omitempty"`
	LifecycleStatus  string     `gorm:"column:lifecycle_status" json:"lifecycleStatus"`
	ActivityState    string     `gorm:"column:activity_state" json:"activityState"`
	HealthState      string     `gorm:"column:health_state" json:"healthState"`
	StartedAt        *time.Time `gorm:"column:started_at" json:"startedAt,omitempty"`
	EndedAt          *time.Time `gorm:"column:ended_at" json:"endedAt,omitempty"`
	ChildCount       int64      `gorm:"column:child_count" json:"childCount"`
	InputTokens      int64      `gorm:"column:input_tokens" json:"inputTokens"`
	OutputTokens     int64      `gorm:"column:output_tokens" json:"outputTokens"`
	ReasoningTokens  int64      `gorm:"column:reasoning_tokens" json:"reasoningTokens"`
	CacheReadTokens  int64      `gorm:"column:cache_read_tokens" json:"cacheReadTokens"`
	CacheWriteTokens int64      `gorm:"column:cache_write_tokens" json:"cacheWriteTokens"`
	TotalTokens      int64      `gorm:"column:total_tokens" json:"totalTokens"`
	CostUSD          float64    `gorm:"column:cost_usd" json:"costUsd"`
}

func (SessionAgent) TableName() string { return "captain_session_agents" }

// SessionCost groups actual model calls by model/backend/effort for one agent.
type SessionCost struct {
	ID               string     `gorm:"column:id" json:"id"`
	SessionID        uuid.UUID  `gorm:"column:session_id" json:"sessionId"`
	Model            string     `gorm:"column:model" json:"model"`
	Backend          string     `gorm:"column:backend" json:"backend"`
	Effort           *string    `gorm:"column:effort" json:"effort,omitempty"`
	Currency         string     `gorm:"column:currency" json:"currency"`
	ModelCallCount   int64      `gorm:"column:model_call_count" json:"modelCallCount"`
	InputTokens      int64      `gorm:"column:input_tokens" json:"inputTokens"`
	OutputTokens     int64      `gorm:"column:output_tokens" json:"outputTokens"`
	ReasoningTokens  int64      `gorm:"column:reasoning_tokens" json:"reasoningTokens"`
	CacheReadTokens  int64      `gorm:"column:cache_read_tokens" json:"cacheReadTokens"`
	CacheWriteTokens int64      `gorm:"column:cache_write_tokens" json:"cacheWriteTokens"`
	TotalTokens      int64      `gorm:"column:total_tokens" json:"totalTokens"`
	TotalCost        float64    `gorm:"column:total_cost" json:"totalCost"`
	FirstCallAt      *time.Time `gorm:"column:first_call_at" json:"firstCallAt,omitempty"`
	LastCallAt       *time.Time `gorm:"column:last_call_at" json:"lastCallAt,omitempty"`
}

func (SessionCost) TableName() string { return "captain_session_costs" }

func (db *DB) ListThreadSessionOverviews(ctx context.Context, rootID uuid.UUID) ([]SessionOverview, error) {
	if rootID == uuid.Nil {
		return nil, fmt.Errorf("%w: thread root ID is required", ErrInvalidSession)
	}
	var rows []SessionOverview
	if err := db.gorm.WithContext(ctx).Where("id = ? OR root_session_id = ?", rootID, rootID).
		Order("parent_session_id NULLS FIRST, COALESCE(last_activity_at, started_at, created_at), id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Captain thread sessions: %w", err)
	}
	return rows, nil
}

// threadScopePredicate restricts a thread-scoped table or view to one root
// session plus its subagents. `= ANY (ARRAY(...))` rather than `IN (...)`: the
// subquery form plans as a semi-join, which Postgres can only apply as a filter
// above the join, forcing a full scan of captain_messages. The array form is an
// InitPlan producing a run-time constant, so both OR arms stay index conditions.
const threadScopePredicate = "session_id = ? OR session_id = ANY (ARRAY(SELECT id FROM captain_sessions WHERE root_session_id = ?))"

func (db *DB) ListThreadTranscriptMessages(ctx context.Context, rootID uuid.UUID) ([]TranscriptMessage, error) {
	if rootID == uuid.Nil {
		return nil, fmt.Errorf("%w: thread root ID is required", ErrInvalidSession)
	}
	var rows []TranscriptMessage
	if err := db.gorm.WithContext(ctx).Where(threadScopePredicate, rootID, rootID).
		Order("occurred_at NULLS LAST, session_id, sequence").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Captain thread transcript: %w", err)
	}
	return rows, nil
}

func (db *DB) ListThreadTurns(ctx context.Context, rootID uuid.UUID) ([]SessionTurn, error) {
	var rows []SessionTurn
	if err := db.threadQuery(ctx, rootID, &SessionTurn{}).Order("started_at NULLS LAST, session_id, turn_index").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Captain thread turns: %w", err)
	}
	return rows, nil
}

func (db *DB) ListThreadAgents(ctx context.Context, rootID uuid.UUID) ([]SessionAgent, error) {
	var rows []SessionAgent
	if err := db.threadQuery(ctx, rootID, &SessionAgent{}).Order("is_root DESC, started_at NULLS LAST, session_id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Captain thread agents: %w", err)
	}
	return rows, nil
}

func (db *DB) ListThreadCosts(ctx context.Context, rootID uuid.UUID) ([]SessionCost, error) {
	var rows []SessionCost
	if err := db.threadQuery(ctx, rootID, &SessionCost{}).Order("first_call_at NULLS LAST, model, backend").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Captain thread costs: %w", err)
	}
	return rows, nil
}

func (db *DB) threadQuery(ctx context.Context, rootID uuid.UUID, model any) *gorm.DB {
	query := db.gorm.WithContext(ctx).Model(model)
	if rootID == uuid.Nil {
		return query.Where("1 = 0")
	}
	return query.Where(threadScopePredicate, rootID, rootID)
}
