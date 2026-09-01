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
	ModelProvider    *string    `gorm:"column:model_provider" json:"modelProvider,omitempty"`
	ModelMode        *string    `gorm:"column:model_mode" json:"modelMode,omitempty"`
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
	// ProviderCostUSD is how much of CostUSD the providers reported themselves;
	// the bucket costs are what CostUSD otherwise falls back to. Zero provider
	// cost means the total is a reconstruction, not a billed figure.
	ProviderCostUSD float64 `gorm:"column:provider_cost_usd" json:"providerCostUsd,omitempty"`
	InputCost       float64 `gorm:"column:input_cost" json:"inputCost,omitempty"`
	OutputCost      float64 `gorm:"column:output_cost" json:"outputCost,omitempty"`
	ReasoningCost   float64 `gorm:"column:reasoning_cost" json:"reasoningCost,omitempty"`
	CacheReadCost   float64 `gorm:"column:cache_read_cost" json:"cacheReadCost,omitempty"`
	CacheWriteCost  float64 `gorm:"column:cache_write_cost" json:"cacheWriteCost,omitempty"`
}

func (SessionTurn) TableName() string { return "captain_session_turns" }

// SessionAgent is one root or child session in a provider thread.
type SessionAgent struct {
	ID               uuid.UUID             `gorm:"column:id" json:"id"`
	SessionID        uuid.UUID             `gorm:"column:session_id" json:"sessionId"`
	ParentSessionID  *uuid.UUID            `gorm:"column:parent_session_id" json:"parentSessionId,omitempty"`
	ParentRelation   SessionParentRelation `gorm:"column:parent_relation" json:"parentRelation,omitempty"`
	RootSessionID    *uuid.UUID            `gorm:"column:root_session_id" json:"rootSessionId,omitempty"`
	IsRoot           bool                  `gorm:"column:is_root" json:"isRoot"`
	AgentType        *string               `gorm:"column:agent_type" json:"agentType,omitempty"`
	Description      *string               `gorm:"column:description" json:"description,omitempty"`
	HistoryFile      *string               `gorm:"column:history_file" json:"historyFile,omitempty"`
	Source           string                `gorm:"column:source" json:"source"`
	Provider         string                `gorm:"column:provider" json:"provider,omitempty"`
	LifecycleStatus  string                `gorm:"column:lifecycle_status" json:"lifecycleStatus"`
	ActivityState    string                `gorm:"column:activity_state" json:"activityState"`
	HealthState      string                `gorm:"column:health_state" json:"healthState"`
	StartedAt        *time.Time            `gorm:"column:started_at" json:"startedAt,omitempty"`
	EndedAt          *time.Time            `gorm:"column:ended_at" json:"endedAt,omitempty"`
	ChildCount       int64                 `gorm:"column:child_count" json:"childCount"`
	InputTokens      int64                 `gorm:"column:input_tokens" json:"inputTokens"`
	OutputTokens     int64                 `gorm:"column:output_tokens" json:"outputTokens"`
	ReasoningTokens  int64                 `gorm:"column:reasoning_tokens" json:"reasoningTokens"`
	CacheReadTokens  int64                 `gorm:"column:cache_read_tokens" json:"cacheReadTokens"`
	CacheWriteTokens int64                 `gorm:"column:cache_write_tokens" json:"cacheWriteTokens"`
	TotalTokens      int64                 `gorm:"column:total_tokens" json:"totalTokens"`
	CostUSD          float64               `gorm:"column:cost_usd" json:"costUsd"`
}

func (SessionAgent) TableName() string { return "captain_session_agents" }

// SessionCost groups actual model calls by model/provider/mode/effort for one agent.
type SessionCost struct {
	ID               string     `gorm:"column:id" json:"id"`
	SessionID        uuid.UUID  `gorm:"column:session_id" json:"sessionId"`
	Model            string     `gorm:"column:model" json:"model"`
	Provider         *string    `gorm:"column:provider" json:"provider,omitempty"`
	ModelMode        *string    `gorm:"column:model_mode" json:"modelMode,omitempty"`
	Effort           *string    `gorm:"column:effort" json:"effort,omitempty"`
	Currency         string     `gorm:"column:currency" json:"currency"`
	ModelCallCount   int64      `gorm:"column:model_call_count" json:"modelCallCount"`
	InputTokens      int64      `gorm:"column:input_tokens" json:"inputTokens"`
	OutputTokens     int64      `gorm:"column:output_tokens" json:"outputTokens"`
	ReasoningTokens  int64      `gorm:"column:reasoning_tokens" json:"reasoningTokens"`
	CacheReadTokens  int64      `gorm:"column:cache_read_tokens" json:"cacheReadTokens"`
	CacheWriteTokens int64      `gorm:"column:cache_write_tokens" json:"cacheWriteTokens"`
	TotalTokens      int64      `gorm:"column:total_tokens" json:"totalTokens"`
	InputCost        float64    `gorm:"column:input_cost" json:"inputCost"`
	OutputCost       float64    `gorm:"column:output_cost" json:"outputCost"`
	ReasoningCost    float64    `gorm:"column:reasoning_cost" json:"reasoningCost"`
	CacheReadCost    float64    `gorm:"column:cache_read_cost" json:"cacheReadCost"`
	CacheWriteCost   float64    `gorm:"column:cache_write_cost" json:"cacheWriteCost"`
	TotalCost        float64    `gorm:"column:total_cost" json:"totalCost"`
	ProviderCostUSD  float64    `gorm:"column:provider_cost_usd" json:"providerCostUsd,omitempty"`
	FirstCallAt      *time.Time `gorm:"column:first_call_at" json:"firstCallAt,omitempty"`
	LastCallAt       *time.Time `gorm:"column:last_call_at" json:"lastCallAt,omitempty"`
}

func (SessionCost) TableName() string { return "captain_session_costs" }

func (db *DB) ListThreadSessionOverviews(ctx context.Context, rootID uuid.UUID) ([]SessionOverview, error) {
	if rootID == uuid.Nil {
		return nil, fmt.Errorf("%w: thread root ID is required", ErrInvalidSession)
	}
	var rows []SessionOverview
	if err := db.gorm.WithContext(ctx).Where(threadSessionScopePredicate, rootID, rootID, rootID).
		Order("parent_session_id NULLS FIRST, COALESCE(last_activity_at, started_at, created_at), id").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Captain thread sessions: %w", err)
	}
	return rows, nil
}

// threadScopePredicate restricts a thread-scoped table or view to one root
// session plus its agent descendants. Transcript-owned branches mirror provider
// history for recovery and must not be counted a second time. `= ANY
// (ARRAY(...))` rather than `IN (...)`: the
// subquery form plans as a semi-join, which Postgres can only apply as a filter
// above the join, forcing a full scan of captain_messages. The array form is an
// InitPlan producing a run-time constant, so both OR arms stay index conditions.
const threadDescendantIDs = `ARRAY(
	WITH RECURSIVE scoped_sessions AS (
		SELECT id
		FROM captain_sessions
		WHERE root_session_id = ?
		  AND (parent_session_id = ? OR parent_session_id IS NULL)
		  AND parent_relation IS DISTINCT FROM 'transcript'
		UNION ALL
		SELECT child.id
		FROM captain_sessions child
		JOIN scoped_sessions parent ON child.parent_session_id = parent.id
		WHERE child.parent_relation IS DISTINCT FROM 'transcript'
	)
	SELECT id FROM scoped_sessions
)`

const threadScopePredicate = "session_id = ? OR session_id = ANY (" + threadDescendantIDs + ")"
const threadSessionScopePredicate = "id = ? OR id = ANY (" + threadDescendantIDs + ")"

func (db *DB) ListThreadTranscriptMessages(ctx context.Context, rootID uuid.UUID) ([]TranscriptMessage, error) {
	if rootID == uuid.Nil {
		return nil, fmt.Errorf("%w: thread root ID is required", ErrInvalidSession)
	}
	var rows []TranscriptMessage
	if err := db.gorm.WithContext(ctx).Where(threadScopePredicate, rootID, rootID, rootID).
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
	if err := db.threadQuery(ctx, rootID, &SessionCost{}).Order("first_call_at NULLS LAST, model, provider, mode").Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("list Captain thread costs: %w", err)
	}
	return rows, nil
}

func (db *DB) threadQuery(ctx context.Context, rootID uuid.UUID, model any) *gorm.DB {
	query := db.gorm.WithContext(ctx).Model(model)
	if rootID == uuid.Nil {
		return query.Where("1 = 0")
	}
	return query.Where(threadScopePredicate, rootID, rootID, rootID)
}
