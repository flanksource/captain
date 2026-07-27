package database

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type PromptRunRuntimeSelection struct {
	Provider string `json:"provider,omitempty"`
	Backend  string `json:"backend,omitempty"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

type PromptRunRuntime struct {
	Mode      string                    `json:"mode,omitempty"`
	Driver    string                    `json:"driver,omitempty"`
	Requested PromptRunRuntimeSelection `json:"requested,omitempty"`
	Resolved  PromptRunRuntimeSelection `json:"resolved,omitempty"`
}

type PromptRunDisplayStatus string

const (
	PromptRunStatusRunning   PromptRunDisplayStatus = "running"
	PromptRunStatusPlanning  PromptRunDisplayStatus = "planning"
	PromptRunStatusZombie    PromptRunDisplayStatus = "zombie"
	PromptRunStatusAsk       PromptRunDisplayStatus = "ask"
	PromptRunStatusCompleted PromptRunDisplayStatus = "completed"
	PromptRunStatusFailed    PromptRunDisplayStatus = "failed"
	PromptRunStatusCancelled PromptRunDisplayStatus = "cancelled"
)

type PromptRunOverview struct {
	PromptRun
	ProviderSessionID string                    `json:"providerSessionId,omitempty"`
	Requested         PromptRunRuntimeSelection `json:"requested"`
	Resolved          PromptRunRuntimeSelection `json:"resolved"`
	Provider          string                    `json:"provider,omitempty"`
	Backend           string                    `json:"backend,omitempty"`
	Model             string                    `json:"model,omitempty"`
	Effort            string                    `json:"effort,omitempty"`
	Mode              string                    `json:"mode,omitempty"`
	Driver            string                    `json:"driver,omitempty"`
	Status            PromptRunDisplayStatus    `json:"status"`
	PID               *int64                    `json:"pid,omitempty"`
	DurationMS        *int64                    `json:"durationMs,omitempty"`
	ProcessActive     bool                      `json:"processActive"`
}

type PromptRunOverviewFilter struct {
	IDs       []uuid.UUID
	SessionID *uuid.UUID
	BatchID   *uuid.UUID
}

type promptRunOverviewRecord struct {
	ID                     uuid.UUID       `gorm:"column:id"`
	SessionID              uuid.UUID       `gorm:"column:session_id"`
	RootSessionID          uuid.UUID       `gorm:"column:root_session_id"`
	ExecutionSessionID     *uuid.UUID      `gorm:"column:execution_session_id"`
	BatchID                *uuid.UUID      `gorm:"column:batch_id"`
	ParentRunID            *uuid.UUID      `gorm:"column:parent_run_id"`
	Origin                 *string         `gorm:"column:origin"`
	SpecProfile            *string         `gorm:"column:spec_profile"`
	AdmissionKey           *string         `gorm:"column:admission_key"`
	RenderedSpec           json.RawMessage `gorm:"column:rendered_spec"`
	Runtime                json.RawMessage `gorm:"column:runtime"`
	Phase                  PromptRunPhase  `gorm:"column:phase"`
	State                  PromptRunState  `gorm:"column:state"`
	CurrentIteration       int             `gorm:"column:current_iteration"`
	ResultText             *string         `gorm:"column:result_text"`
	ResultJSON             json.RawMessage `gorm:"column:result_json"`
	Error                  *string         `gorm:"column:error"`
	Version                int64           `gorm:"column:version"`
	QueuedAt               time.Time       `gorm:"column:queued_at"`
	StartedAt              *time.Time      `gorm:"column:started_at"`
	FinishedAt             *time.Time      `gorm:"column:finished_at"`
	CreatedAt              time.Time       `gorm:"column:created_at"`
	UpdatedAt              time.Time       `gorm:"column:updated_at"`
	ProviderSessionID      *string         `gorm:"column:provider_session_id"`
	ExecutionProvider      *string         `gorm:"column:execution_provider"`
	ExecutionBackend       *string         `gorm:"column:execution_backend"`
	ExecutionModel         *string         `gorm:"column:execution_model"`
	ExecutionEffort        *string         `gorm:"column:execution_effort"`
	ExecutionActivity      *string         `gorm:"column:execution_activity"`
	ExecutionHealth        *string         `gorm:"column:execution_health"`
	ExecutionPID           *int64          `gorm:"column:execution_pid"`
	ExecutionProcessActive bool            `gorm:"column:execution_process_active"`
}

func (db *DB) GetPromptRunOverview(ctx context.Context, id uuid.UUID) (*PromptRunOverview, error) {
	rows, err := db.ListPromptRunOverviews(ctx, PromptRunOverviewFilter{IDs: []uuid.UUID{id}})
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrPromptRunNotFound, id)
	}
	return &rows[0], nil
}

func (db *DB) ListPromptRunOverviews(ctx context.Context, filter PromptRunOverviewFilter) ([]PromptRunOverview, error) {
	if err := db.requireGorm(); err != nil {
		return nil, err
	}
	query := db.gorm.WithContext(ctx).
		Table("captain_prompt_runs AS run").
		Select(`run.*, admission.provider_session_id,
			execution.provider AS execution_provider,
			execution.backend AS execution_backend,
			execution.model AS execution_model,
			execution.effort AS execution_effort,
			execution.activity_state AS execution_activity,
			execution.health_state AS execution_health,
			execution.pid AS execution_pid,
			execution.process_active AS execution_process_active`).
		Joins("JOIN captain_sessions AS admission ON admission.id = run.session_id").
		Joins("LEFT JOIN captain_session_overview AS execution ON execution.id = run.execution_session_id").
		Order("run.queued_at DESC, run.id DESC")
	if len(filter.IDs) > 0 {
		for _, id := range filter.IDs {
			if id == uuid.Nil {
				return nil, fmt.Errorf("%w: prompt-run ID filter cannot contain an empty UUID", ErrInvalidPromptRun)
			}
		}
		query = query.Where("run.id IN ?", filter.IDs)
	}
	if filter.SessionID != nil {
		if *filter.SessionID == uuid.Nil {
			return nil, fmt.Errorf("%w: session ID filter cannot be empty", ErrInvalidPromptRun)
		}
		query = query.Where("run.session_id = ?", *filter.SessionID)
	}
	if filter.BatchID != nil {
		if *filter.BatchID == uuid.Nil {
			return nil, fmt.Errorf("%w: batch ID filter cannot be empty", ErrInvalidPromptRun)
		}
		query = query.Where("run.batch_id = ?", *filter.BatchID)
	}
	var records []promptRunOverviewRecord
	if err := query.Scan(&records).Error; err != nil {
		return nil, fmt.Errorf("list Captain prompt-run overviews: %w", err)
	}
	rows := make([]PromptRunOverview, len(records))
	for i := range records {
		row, err := promptRunOverviewFromRecord(records[i])
		if err != nil {
			return nil, fmt.Errorf("project Captain prompt run %s: %w", records[i].ID, err)
		}
		rows[i] = row
	}
	return rows, nil
}

func promptRunOverviewFromRecord(record promptRunOverviewRecord) (PromptRunOverview, error) {
	var runtime PromptRunRuntime
	if len(record.Runtime) > 0 && string(record.Runtime) != "null" {
		if err := json.Unmarshal(record.Runtime, &runtime); err != nil {
			return PromptRunOverview{}, fmt.Errorf("decode runtime: %w", err)
		}
	}
	var renderedSpec map[string]any
	if len(record.RenderedSpec) > 0 {
		if err := json.Unmarshal(record.RenderedSpec, &renderedSpec); err != nil {
			return PromptRunOverview{}, fmt.Errorf("decode rendered spec: %w", err)
		}
	}
	var resultJSON map[string]any
	if len(record.ResultJSON) > 0 && string(record.ResultJSON) != "null" {
		if err := json.Unmarshal(record.ResultJSON, &resultJSON); err != nil {
			return PromptRunOverview{}, fmt.Errorf("decode result JSON: %w", err)
		}
	}
	execution := PromptRunRuntimeSelection{
		Provider: optionalString(record.ExecutionProvider), Backend: optionalString(record.ExecutionBackend),
		Model: optionalString(record.ExecutionModel), Effort: optionalString(record.ExecutionEffort),
	}
	effective := resolvePromptRunRuntimeSelection(runtime.Resolved, runtime.Requested, execution)
	status, err := promptRunDisplayStatus(record.State, runtime.Mode, optionalString(record.ExecutionActivity), optionalString(record.ExecutionHealth))
	if err != nil {
		return PromptRunOverview{}, err
	}
	var durationMS *int64
	if record.StartedAt != nil {
		end := time.Now().UTC()
		if record.FinishedAt != nil {
			end = *record.FinishedAt
		}
		value := end.Sub(*record.StartedAt).Milliseconds()
		durationMS = &value
	}
	pid := record.ExecutionPID
	if !record.ExecutionProcessActive || status == PromptRunStatusZombie || pid == nil || *pid <= 0 {
		pid = nil
	}
	return PromptRunOverview{
		PromptRun: PromptRun{
			ID: record.ID, SessionID: record.SessionID, RootSessionID: record.RootSessionID,
			ExecutionSessionID: record.ExecutionSessionID, BatchID: record.BatchID, ParentRunID: record.ParentRunID,
			Origin: optionalString(record.Origin), SpecProfile: optionalString(record.SpecProfile), AdmissionKey: optionalString(record.AdmissionKey),
			RenderedSpec: renderedSpec, Runtime: runtime, Phase: record.Phase, State: record.State,
			CurrentIteration: record.CurrentIteration, ResultText: optionalString(record.ResultText), ResultJSON: resultJSON,
			Error: optionalString(record.Error), Version: record.Version, QueuedAt: record.QueuedAt,
			StartedAt: record.StartedAt, FinishedAt: record.FinishedAt, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		},
		ProviderSessionID: optionalString(record.ProviderSessionID), Requested: runtime.Requested, Resolved: runtime.Resolved,
		Provider: effective.Provider, Backend: effective.Backend, Model: effective.Model, Effort: effective.Effort,
		Mode: runtime.Mode, Driver: runtime.Driver, Status: status, PID: pid, DurationMS: durationMS,
		ProcessActive: record.ExecutionProcessActive,
	}, nil
}

func resolvePromptRunRuntimeSelection(resolved, requested, execution PromptRunRuntimeSelection) PromptRunRuntimeSelection {
	return PromptRunRuntimeSelection{
		Provider: firstRuntimeValue(resolved.Provider, requested.Provider, execution.Provider),
		Backend:  firstRuntimeValue(resolved.Backend, requested.Backend, execution.Backend),
		Model:    firstRuntimeValue(resolved.Model, requested.Model, execution.Model),
		Effort:   firstRuntimeValue(resolved.Effort, requested.Effort, execution.Effort),
	}
}

func firstRuntimeValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func promptRunDisplayStatus(state PromptRunState, mode, activity, health string) (PromptRunDisplayStatus, error) {
	switch state {
	case PromptRunStateSucceeded:
		return PromptRunStatusCompleted, nil
	case PromptRunStateFailed:
		return PromptRunStatusFailed, nil
	case PromptRunStateCancelled:
		return PromptRunStatusCancelled, nil
	case PromptRunStatePending, PromptRunStateRunning, PromptRunStateWaiting:
	default:
		return "", fmt.Errorf("%w: unknown state %q", ErrInvalidPromptRun, state)
	}
	if health == string(SessionHealthZombie) {
		return PromptRunStatusZombie, nil
	}
	if state == PromptRunStateWaiting || activity == string(SessionActivityAsk) || activity == string(SessionActivityApproval) {
		return PromptRunStatusAsk, nil
	}
	if strings.EqualFold(strings.TrimSpace(mode), "plan") {
		return PromptRunStatusPlanning, nil
	}
	return PromptRunStatusRunning, nil
}
