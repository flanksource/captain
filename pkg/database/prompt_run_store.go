package database

import (
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
)

type PromptRunPhase string

const (
	PromptRunPhaseQueued   PromptRunPhase = "queued"
	PromptRunPhasePreRun   PromptRunPhase = "pre_run"
	PromptRunPhaseGenerate PromptRunPhase = "generate"
	PromptRunPhaseVerify   PromptRunPhase = "verify"
	PromptRunPhaseFeedback PromptRunPhase = "feedback"
	PromptRunPhasePostRun  PromptRunPhase = "post_run"
	PromptRunPhaseOutput   PromptRunPhase = "output"
	PromptRunPhaseFinished PromptRunPhase = "finished"
)

type PromptRunState string

const (
	PromptRunStatePending   PromptRunState = "pending"
	PromptRunStateRunning   PromptRunState = "running"
	PromptRunStateWaiting   PromptRunState = "waiting"
	PromptRunStateSucceeded PromptRunState = "succeeded"
	PromptRunStateFailed    PromptRunState = "failed"
	PromptRunStateCancelled PromptRunState = "cancelled"
)

type PromptRun struct {
	ID                   uuid.UUID              `json:"id"`
	SessionID            uuid.UUID              `json:"sessionId"`
	TurnID               *uuid.UUID             `json:"turnId,omitempty"`
	RootSessionID        uuid.UUID              `json:"rootSessionId"`
	ExecutionSessionID   *uuid.UUID             `json:"executionSessionId,omitempty"`
	BatchID              *uuid.UUID             `json:"batchId,omitempty"`
	ParentRunID          *uuid.UUID             `json:"parentRunId,omitempty"`
	InputPlanID          *uuid.UUID             `json:"inputPlanId,omitempty"`
	InputPlanRevisionID  *uuid.UUID             `json:"inputPlanRevisionId,omitempty"`
	Origin               string                 `json:"origin,omitempty"`
	SpecProfile          string                 `json:"specProfile,omitempty"`
	AdmissionKey         string                 `json:"admissionKey,omitempty"`
	RenderedSpec         map[string]any         `json:"renderedSpec,omitempty"`
	Runtime              PromptRunRuntime       `json:"runtime,omitempty"`
	PromptMarkdown       string                 `json:"promptMarkdown,omitempty"`
	VerificationMarkdown string                 `json:"verificationMarkdown,omitempty"`
	Phase                PromptRunPhase         `json:"phase"`
	State                PromptRunState         `json:"state"`
	CurrentIteration     int                    `json:"currentIteration"`
	ResultText           string                 `json:"resultText,omitempty"`
	ResultJSON           map[string]any         `json:"resultJson,omitempty"`
	ApprovalState        *api.ToolApprovalState `json:"-"`
	ProviderCheckpoint   *PromptRunCheckpoint   `json:"-"`
	Error                string                 `json:"error,omitempty"`
	Version              int64                  `json:"version"`
	QueuedAt             time.Time              `json:"queuedAt"`
	StartedAt            *time.Time             `json:"startedAt,omitempty"`
	FinishedAt           *time.Time             `json:"finishedAt,omitempty"`
	CreatedAt            time.Time              `json:"createdAt"`
	UpdatedAt            time.Time              `json:"updatedAt"`
}

type PromptRunCheckpoint struct {
	Codec   string
	Version int
	Payload []byte
}

// PromptRunFilter limits ListPromptRuns. Nil fields are not filtered.
type PromptRunFilter struct {
	SessionID *uuid.UUID
	State     *PromptRunState
}

type CreatePromptRunInput struct {
	ID                   uuid.UUID
	SessionID            uuid.UUID
	TurnID               *uuid.UUID
	RootSessionID        *uuid.UUID
	ExecutionSessionID   *uuid.UUID
	BatchID              *uuid.UUID
	ParentRunID          *uuid.UUID
	InputPlanID          *uuid.UUID
	InputPlanRevisionID  *uuid.UUID
	Origin               string
	SpecProfile          string
	AdmissionKey         string
	RenderedSpec         map[string]any
	Runtime              PromptRunRuntime
	PromptMarkdown       string
	VerificationMarkdown string
}

type UpdatePromptRunInput struct {
	ID                      uuid.UUID
	ExpectedVersion         int64
	Phase                   *PromptRunPhase
	State                   *PromptRunState
	CurrentIteration        *int
	ExecutionSessionID      *uuid.UUID
	RenderedSpec            *map[string]any
	Runtime                 *PromptRunRuntime
	ResultText              *string
	ResultJSON              *map[string]any
	ApprovalState           *api.ToolApprovalState
	ClearApprovalState      bool
	ProviderCheckpoint      *PromptRunCheckpoint
	ClearProviderCheckpoint bool
	Error                   *string
}

type promptRunRecord struct {
	ID                        uuid.UUID              `gorm:"column:id;type:uuid;primaryKey"`
	SessionID                 uuid.UUID              `gorm:"column:session_id;type:uuid"`
	TurnID                    *uuid.UUID             `gorm:"column:turn_id;type:uuid"`
	RootSessionID             uuid.UUID              `gorm:"column:root_session_id;type:uuid"`
	ExecutionSessionID        *uuid.UUID             `gorm:"column:execution_session_id;type:uuid"`
	BatchID                   *uuid.UUID             `gorm:"column:batch_id;type:uuid"`
	ParentRunID               *uuid.UUID             `gorm:"column:parent_run_id;type:uuid"`
	InputPlanID               *uuid.UUID             `gorm:"column:input_plan_id;type:uuid"`
	InputPlanRevisionID       *uuid.UUID             `gorm:"column:input_plan_revision_id;type:uuid"`
	Origin                    *string                `gorm:"column:origin"`
	SpecProfile               *string                `gorm:"column:spec_profile"`
	AdmissionKey              *string                `gorm:"column:admission_key"`
	RenderedSpec              map[string]any         `gorm:"column:rendered_spec;serializer:json;type:jsonb"`
	Runtime                   PromptRunRuntime       `gorm:"column:runtime;serializer:json;type:jsonb"`
	PromptMarkdown            *string                `gorm:"column:prompt_markdown"`
	VerificationMarkdown      *string                `gorm:"column:verification_markdown"`
	Phase                     PromptRunPhase         `gorm:"column:phase"`
	State                     PromptRunState         `gorm:"column:state"`
	CurrentIteration          int                    `gorm:"column:current_iteration"`
	ResultText                *string                `gorm:"column:result_text"`
	ResultJSON                map[string]any         `gorm:"column:result_json;serializer:json;type:jsonb"`
	ApprovalState             *api.ToolApprovalState `gorm:"column:approval_state;serializer:json;type:jsonb"`
	ProviderCheckpointCodec   *string                `gorm:"column:provider_checkpoint_codec"`
	ProviderCheckpointVersion *int                   `gorm:"column:provider_checkpoint_version"`
	ProviderCheckpoint        []byte                 `gorm:"column:provider_checkpoint"`
	Error                     *string                `gorm:"column:error"`
	Version                   int64                  `gorm:"column:version"`
	QueuedAt                  time.Time              `gorm:"column:queued_at"`
	StartedAt                 *time.Time             `gorm:"column:started_at"`
	FinishedAt                *time.Time             `gorm:"column:finished_at"`
	CreatedAt                 time.Time              `gorm:"column:created_at"`
	UpdatedAt                 time.Time              `gorm:"column:updated_at"`
}

func (promptRunRecord) TableName() string { return "captain_prompt_runs" }

// CreatePromptRun creates a run and returns its authoritative UUID. AdmissionKey
