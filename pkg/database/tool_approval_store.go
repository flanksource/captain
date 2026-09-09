package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrTurnRequestInvalid  = errors.New("invalid Captain turn request")
	ErrTurnRequestNotFound = errors.New("captain turn request not found")
	ErrTurnRequestConflict = errors.New("captain turn request conflict")
)

type TurnRequestState string

const (
	TurnRequestStatePending   TurnRequestState = "pending"
	TurnRequestStateApproved  TurnRequestState = "approved"
	TurnRequestStateDenied    TurnRequestState = "denied"
	TurnRequestStateCancelled TurnRequestState = "cancelled"
	TurnRequestStateExpired   TurnRequestState = "expired"
)

type TurnRequest struct {
	ID             uuid.UUID        `json:"id"`
	SessionID      uuid.UUID        `json:"sessionId"`
	TurnID         *uuid.UUID       `json:"turnId,omitempty"`
	PromptRunID    *uuid.UUID       `json:"promptRunId,omitempty"`
	ModelCallID    *uuid.UUID       `json:"modelCallId,omitempty"`
	CredentialID   *uuid.UUID       `json:"-"`
	ToolCallID     string           `json:"toolCallId,omitempty"`
	Kind           string           `json:"kind"`
	State          TurnRequestState `json:"state"`
	Request        map[string]any   `json:"request"`
	Response       map[string]any   `json:"response,omitempty"`
	IdempotencyKey string           `json:"idempotencyKey,omitempty"`
	RequestedBy    string           `json:"requestedBy,omitempty"`
	ResolvedBy     string           `json:"resolvedBy,omitempty"`
	Reason         string           `json:"reason,omitempty"`
	Version        int64            `json:"version"`
	ExpiresAt      *time.Time       `json:"expiresAt,omitempty"`
	CreatedAt      time.Time        `json:"createdAt"`
	ResolvedAt     *time.Time       `json:"resolvedAt,omitempty"`
}

type turnRequestRecord struct {
	ID             uuid.UUID        `gorm:"column:id;type:uuid;primaryKey"`
	SessionID      uuid.UUID        `gorm:"column:session_id;type:uuid"`
	TurnID         *uuid.UUID       `gorm:"column:turn_id;type:uuid"`
	PromptRunID    *uuid.UUID       `gorm:"column:prompt_run_id;type:uuid"`
	ModelCallID    *uuid.UUID       `gorm:"column:model_call_id;type:uuid"`
	CredentialID   *uuid.UUID       `gorm:"column:credential_id;type:uuid"`
	ToolCallID     *string          `gorm:"column:tool_call_id"`
	Kind           string           `gorm:"column:kind"`
	State          TurnRequestState `gorm:"column:state"`
	Request        map[string]any   `gorm:"column:request;serializer:json;type:jsonb"`
	Response       map[string]any   `gorm:"column:response;serializer:json;type:jsonb"`
	IdempotencyKey *string          `gorm:"column:idempotency_key"`
	RequestedBy    *string          `gorm:"column:requested_by"`
	ResolvedBy     *string          `gorm:"column:resolved_by"`
	Reason         *string          `gorm:"column:reason"`
	Version        int64            `gorm:"column:version"`
	ExpiresAt      *time.Time       `gorm:"column:expires_at"`
	CreatedAt      time.Time        `gorm:"column:created_at"`
	ResolvedAt     *time.Time       `gorm:"column:resolved_at"`
}

func (turnRequestRecord) TableName() string { return "captain_turn_requests" }

// CreateToolApprovalRequestInput describes one durable tool approval. TurnID and
// ModelCallID are required on the caller-tool path (CredentialID set) and
// optional on the credential-less provider path, where a streaming provider or
// an external host has a session and a prompt run but never opens a turn or a
// model call. uuid.Nil writes NULL.
type CreateToolApprovalRequestInput struct {
	CredentialID uuid.UUID
	SessionID    uuid.UUID
	TurnID       uuid.UUID
	PromptRunID  uuid.UUID
	ModelCallID  uuid.UUID
	ToolCallID   string
	Tool         string
	Input        map[string]any
	RequestedBy  string
	ExpiresAt    time.Time
}

func (db *DB) CreateToolApprovalRequest(
	ctx context.Context,
	input CreateToolApprovalRequestInput,
) (*TurnRequest, error) {
	var credential *CallerToolCredential
	if input.CredentialID != uuid.Nil {
		if err := db.ValidateCallerToolCredential(ctx, input.CredentialID); err != nil {
			return nil, err
		}
		var err error
		credential, err = db.GetCallerToolCredential(ctx, input.CredentialID)
		if err != nil {
			return nil, err
		}
		if credential.SessionID != input.SessionID || credential.PromptRunID != input.PromptRunID {
			return nil, fmt.Errorf("%w: credential does not belong to the supplied session and run", ErrTurnRequestInvalid)
		}
	}
	input.ToolCallID = strings.TrimSpace(input.ToolCallID)
	input.Tool = strings.TrimSpace(input.Tool)
	if input.SessionID == uuid.Nil || input.PromptRunID == uuid.Nil ||
		input.ToolCallID == "" || input.Tool == "" || !input.ExpiresAt.After(time.Now()) {
		return nil, fmt.Errorf("%w: session, prompt run, tool call, tool, and future expiry are required", ErrTurnRequestInvalid)
	}
	if credential != nil && (input.TurnID == uuid.Nil || input.ModelCallID == uuid.Nil) {
		return nil, fmt.Errorf("%w: a caller-tool approval requires its turn and model call", ErrTurnRequestInvalid)
	}
	if credential != nil && credential.Policy[input.Tool] != api.ToolPolicyAsk {
		return nil, fmt.Errorf("%w: tool %q is not approved by ask policy", ErrTurnRequestInvalid, input.Tool)
	}
	if credential != nil && credential.ExpiresAt != nil && input.ExpiresAt.After(*credential.ExpiresAt) {
		input.ExpiresAt = *credential.ExpiresAt
	}
	idempotencyKey := "provider:" + input.PromptRunID.String() + ":" + input.ToolCallID
	if credential != nil {
		idempotencyKey = "mcp:" + input.CredentialID.String() + ":" + input.ToolCallID
	}
	request := map[string]any{
		"tool": input.Tool, "input": input.Input,
	}
	var credentialID *uuid.UUID
	if credential != nil {
		credentialID = &input.CredentialID
	}
	record := turnRequestRecord{
		ID: uuid.New(), SessionID: input.SessionID, TurnID: nullableUUID(input.TurnID), PromptRunID: &input.PromptRunID,
		ModelCallID: nullableUUID(input.ModelCallID), CredentialID: credentialID, ToolCallID: &input.ToolCallID,
		Kind: "tool_approval", State: TurnRequestStatePending, Request: request,
		IdempotencyKey: &idempotencyKey, RequestedBy: nullableTrimmed(input.RequestedBy), ExpiresAt: &input.ExpiresAt,
	}
	result := db.gorm.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return nil, fmt.Errorf("create tool approval request: %w", result.Error)
	}
	if result.RowsAffected == 1 {
		if err := db.touchChatSession(ctx, input.SessionID); err != nil {
			return nil, err
		}
		return db.GetTurnRequest(ctx, record.ID)
	}
	var existing turnRequestRecord
	if err := db.gorm.WithContext(ctx).
		Where("session_id = ? AND idempotency_key = ?", input.SessionID, idempotencyKey).
		First(&existing).Error; err != nil {
		return nil, fmt.Errorf("read existing tool approval request: %w", err)
	}
	if !reflect.DeepEqual(existing.Request, request) {
		return nil, fmt.Errorf("%w: tool call %q was retried with different input", ErrTurnRequestConflict, input.ToolCallID)
	}
	out := turnRequestFromRecord(existing)
	return &out, nil
}

type ResolveToolApprovalRequestInput struct {
	SessionID      uuid.UUID
	RequestID      uuid.UUID
	ExpectedTurnID *uuid.UUID
	Approved       bool
	UpdatedInput   map[string]any
	ResolvedBy     string
	Reason         string
}

func (db *DB) ResolveToolApprovalRequest(
	ctx context.Context,
	input ResolveToolApprovalRequestInput,
) (*TurnRequest, error) {
	if input.SessionID == uuid.Nil || input.RequestID == uuid.Nil {
		return nil, fmt.Errorf("%w: session and approval request IDs are required", ErrTurnRequestInvalid)
	}
	state := TurnRequestStateDenied
	var response map[string]any
	if input.Approved {
		state = TurnRequestStateApproved
		if input.UpdatedInput != nil {
			response = map[string]any{"updatedInput": input.UpdatedInput}
		}
	}
	var pending turnRequestRecord
	if err := db.gorm.WithContext(ctx).
		Where("id = ? AND session_id = ? AND kind = 'tool_approval'", input.RequestID, input.SessionID).
		First(&pending).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: session %s approval %s", ErrTurnRequestNotFound, input.SessionID, input.RequestID)
		}
		return nil, fmt.Errorf("read tool approval request: %w", err)
	}
	if pending.State != TurnRequestStatePending {
		if pending.State == state && reflect.DeepEqual(pending.Response, response) &&
			optionalString(pending.Reason) == strings.TrimSpace(input.Reason) {
			out := turnRequestFromRecord(pending)
			return &out, nil
		}
		return nil, fmt.Errorf("%w: approval %s already has a different %s decision", ErrTurnRequestConflict, pending.ID, pending.State)
	}
	if input.ExpectedTurnID != nil && (pending.TurnID == nil || *pending.TurnID != *input.ExpectedTurnID) {
		return nil, fmt.Errorf("%w: approval %s does not belong to active turn %s", ErrTurnRequestConflict, pending.ID, *input.ExpectedTurnID)
	}
	now := time.Now().UTC()
	result := db.gorm.WithContext(ctx).Model(&turnRequestRecord{}).
		Where("id = ? AND state = 'pending'", pending.ID).
		Where(`credential_id IS NOT NULL OR EXISTS (
			SELECT 1 FROM captain_prompt_runs run
			WHERE run.id = captain_turn_requests.prompt_run_id AND run.state = 'waiting'
		)`).
		Where(`credential_id IS NULL OR EXISTS (
			SELECT 1 FROM captain_session_mcp_credentials credential
			WHERE credential.id = captain_turn_requests.credential_id
			  AND credential.revoked_at IS NULL
			  AND (credential.expires_at IS NULL OR credential.expires_at > ?)
		)`, now).
		Updates(map[string]any{
			"state": state, "response": response, "resolved_by": nullableTrimmed(input.ResolvedBy),
			"reason": nullableTrimmed(input.Reason), "resolved_at": now,
		})
	if result.Error != nil {
		return nil, fmt.Errorf("resolve tool approval request: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		if pending.CredentialID == nil {
			return nil, fmt.Errorf("%w: approval %s cannot be resolved before its prompt run is waiting", ErrTurnRequestConflict, pending.ID)
		}
		if pending.CredentialID != nil {
			if err := db.ValidateCallerToolCredential(ctx, *pending.CredentialID); err != nil {
				return nil, err
			}
		}
		return nil, fmt.Errorf("%w: session %s approval %s", ErrTurnRequestNotFound, input.SessionID, input.RequestID)
	}
	if err := db.touchChatSession(ctx, input.SessionID); err != nil {
		return nil, err
	}
	return db.GetTurnRequest(ctx, pending.ID)
}

func (db *DB) GetTurnRequest(ctx context.Context, id uuid.UUID) (*TurnRequest, error) {
	var record turnRequestRecord
	if err := db.gorm.WithContext(ctx).First(&record, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: %s", ErrTurnRequestNotFound, id)
		}
		return nil, fmt.Errorf("get Captain turn request: %w", err)
	}
	out := turnRequestFromRecord(record)
	return &out, nil
}

type TurnRequestFilter struct {
	SessionID   uuid.UUID
	PromptRunID *uuid.UUID
}

func (db *DB) ListTurnRequests(ctx context.Context, filter TurnRequestFilter) ([]TurnRequest, error) {
	if filter.SessionID == uuid.Nil {
		return nil, fmt.Errorf("%w: session ID is required", ErrTurnRequestInvalid)
	}
	query := db.gorm.WithContext(ctx).Where("session_id = ?", filter.SessionID).Order("created_at, id")
	if filter.PromptRunID != nil {
		query = query.Where("prompt_run_id = ?", *filter.PromptRunID)
	}
	var records []turnRequestRecord
	if err := query.Find(&records).Error; err != nil {
		return nil, fmt.Errorf("list Captain turn requests: %w", err)
	}
	requests := make([]TurnRequest, len(records))
	for i := range records {
		requests[i] = turnRequestFromRecord(records[i])
	}
	return requests, nil
}

// CountPendingToolApprovals reports how many tool approvals a prompt run is
// still blocked on.
//
// It is the predicate behind the waiting posture. A run raises one approval per
// tool call, so a turn that issues parallel calls has several outstanding at
// once; "is this run waiting" is a property of the set, not of whichever wait
// happens to finish first. Counting in SQL keeps the answer true at read time
// rather than tracked in a process that may not be the only one writing.
func (db *DB) CountPendingToolApprovals(ctx context.Context, promptRunID uuid.UUID) (int64, error) {
	if promptRunID == uuid.Nil {
		return 0, fmt.Errorf("%w: prompt run ID is required", ErrTurnRequestInvalid)
	}
	var pending int64
	err := db.gorm.WithContext(ctx).Model(&turnRequestRecord{}).
		Where("prompt_run_id = ? AND kind = 'tool_approval' AND state = ?",
			promptRunID, TurnRequestStatePending).
		Count(&pending).Error
	if err != nil {
		return 0, fmt.Errorf("count pending Captain tool approvals: %w", err)
	}
	return pending, nil
}

// UnwaitedPromptRun is a prompt run whose state claims it is progressing while
// a tool approval is still holding it.
//
// The pair is contradictory and self-perpetuating: ResolveToolApprovalRequest
// only accepts a credential-less approval while its prompt run is waiting, so a
// run in this state cannot be answered back out of it. Whatever produced the
// divergence — a host that un-waited the run while siblings were outstanding, a
// process that died between the two writes — the row is the evidence and the
// only exit is the approval's own expiry.
type UnwaitedPromptRun struct {
	ID        uuid.UUID
	SessionID uuid.UUID
	Pending   int64
}

// ListUnwaitedPromptRuns returns the runs whose state and outstanding approvals
// disagree, so a sweeper can restore the waiting posture the approvals imply.
func (db *DB) ListUnwaitedPromptRuns(ctx context.Context) ([]UnwaitedPromptRun, error) {
	var rows []UnwaitedPromptRun
	err := db.gorm.WithContext(ctx).Raw(`
		SELECT run.id AS id, run.session_id AS session_id, count(*) AS pending
		FROM captain_prompt_runs run
		JOIN captain_turn_requests request ON request.prompt_run_id = run.id
		WHERE run.state = 'running'
		  AND request.kind = 'tool_approval'
		  AND request.state = 'pending'
		GROUP BY run.id, run.session_id
		ORDER BY run.id`).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list unwaited Captain prompt runs: %w", err)
	}
	return rows, nil
}

// RestorePromptRunToWaiting moves one run back to waiting, but only while the
// database still shows the approval that justifies it.
//
// The predicate and the write have to be the same statement. Evaluate them
// separately and the last approval can be answered in between: the broker's own
// resume moves the run to running and the trigger bumps its version, so a
// re-read supplies exactly the version an optimistic write expects and the
// restore lands anyway. That parks a run nobody is waiting on — no pending
// approval left to answer, no wait left to call OnRunning — and neither half of
// the sweep moves a run out of waiting, so nothing ever repairs it.
//
// Reports whether the row moved. Not moving is the ordinary outcome rather than
// an error: it means the run stopped needing the restore.
func (db *DB) RestorePromptRunToWaiting(ctx context.Context, promptRunID uuid.UUID) (bool, error) {
	if promptRunID == uuid.Nil {
		return false, fmt.Errorf("%w: prompt run ID is required", ErrTurnRequestInvalid)
	}
	result := db.gorm.WithContext(ctx).Exec(`
		UPDATE captain_prompt_runs SET state = ?
		WHERE id = ?
		  AND state = ?
		  AND EXISTS (
		    SELECT 1 FROM captain_turn_requests
		    WHERE prompt_run_id = captain_prompt_runs.id
		      AND kind = 'tool_approval'
		      AND state = ?)`,
		PromptRunStateWaiting, promptRunID, PromptRunStateRunning, TurnRequestStatePending)
	if result.Error != nil {
		return false, fmt.Errorf("restore Captain prompt run to waiting: %w", result.Error)
	}
	return result.RowsAffected > 0, nil
}

// StaleToolApproval is one pending tool approval that no wait can still answer,
// as a sweeper running outside the process that raised it sees it.
type StaleToolApproval struct {
	ID          uuid.UUID
	SessionID   uuid.UUID
	PromptRunID *uuid.UUID
	Tool        string
	RequestedBy string
	CreatedAt   time.Time
	ExpiresAt   *time.Time
	RunState    PromptRunState
	// Lapsed distinguishes the two reasons, which are not the same event: the
	// approval's own window closed with nobody answering (expired), or the prompt
	// run it blocks ended and took the question with it (cancelled).
	Lapsed bool
}

type staleToolApprovalRow struct {
	ID          uuid.UUID  `gorm:"column:id"`
	SessionID   uuid.UUID  `gorm:"column:session_id"`
	PromptRunID *uuid.UUID `gorm:"column:prompt_run_id"`
	Tool        string     `gorm:"column:tool"`
	RequestedBy string     `gorm:"column:requested_by"`
	CreatedAt   time.Time  `gorm:"column:created_at"`
	ExpiresAt   *time.Time `gorm:"column:expires_at"`
	RunState    string     `gorm:"column:run_state"`
	Lapsed      bool       `gorm:"column:lapsed"`
}

// ListStaleToolApprovals returns the pending tool approvals a sweeper may
// terminate as of now.
//
// It exists because expires_at was, until it had this reader, data nobody
// enforced: the only code that looked at it lived inside the broker's own wait
// loop, so an approval outlived the process that raised it — a crash, a serve
// restart, a laptop closing — and stayed `pending` with a timestamp long past.
//
// Two shapes qualify, and nothing else does. A live pending row inside its own
// window belongs to whoever is looking at it, and a row that already reached a
// decision keeps that decision; re-terminating either would destroy an answer.
func (db *DB) ListStaleToolApprovals(ctx context.Context, now time.Time) ([]StaleToolApproval, error) {
	var rows []staleToolApprovalRow
	err := db.gorm.WithContext(ctx).Raw(`
		SELECT request.id, request.session_id, request.prompt_run_id,
		       COALESCE(request.request->>'tool', '') AS tool,
		       COALESCE(request.requested_by, '') AS requested_by,
		       request.created_at, request.expires_at,
		       COALESCE(run.state::text, '') AS run_state,
		       (request.expires_at IS NOT NULL AND request.expires_at <= ?) AS lapsed
		FROM captain_turn_requests request
		LEFT JOIN captain_prompt_runs run ON run.id = request.prompt_run_id
		WHERE request.kind = 'tool_approval'
		  AND request.state = 'pending'
		  AND ((request.expires_at IS NOT NULL AND request.expires_at <= ?)
		    OR run.state IN ('succeeded', 'failed', 'cancelled'))
		ORDER BY request.created_at, request.id`, now, now).Scan(&rows).Error
	if err != nil {
		return nil, fmt.Errorf("list stale Captain tool approvals: %w", err)
	}
	stale := make([]StaleToolApproval, len(rows))
	for i, row := range rows {
		stale[i] = StaleToolApproval{
			ID: row.ID, SessionID: row.SessionID, PromptRunID: row.PromptRunID,
			Tool: row.Tool, RequestedBy: row.RequestedBy, CreatedAt: row.CreatedAt,
			ExpiresAt: row.ExpiresAt, RunState: PromptRunState(row.RunState), Lapsed: row.Lapsed,
		}
	}
	return stale, nil
}

func (db *DB) ExpireToolApprovalRequest(ctx context.Context, id uuid.UUID, state TurnRequestState, reason string) error {
	if state != TurnRequestStateExpired && state != TurnRequestStateCancelled {
		return fmt.Errorf("%w: terminal state %q is invalid", ErrTurnRequestInvalid, state)
	}
	now := time.Now().UTC()
	result := db.gorm.WithContext(ctx).Model(&turnRequestRecord{}).
		Where("id = ? AND state = 'pending'", id).
		Updates(map[string]any{"state": state, "reason": nullableTrimmed(reason), "resolved_at": now})
	if result.Error != nil {
		return fmt.Errorf("expire tool approval request: %w", result.Error)
	}
	return nil
}

func (db *DB) CancelPendingTurnRequests(ctx context.Context, sessionID, promptRunID uuid.UUID, reason string) error {
	if sessionID == uuid.Nil || promptRunID == uuid.Nil {
		return fmt.Errorf("%w: session and prompt run IDs are required", ErrTurnRequestInvalid)
	}
	now := time.Now().UTC()
	result := db.gorm.WithContext(ctx).Model(&turnRequestRecord{}).
		Where("session_id = ? AND prompt_run_id = ? AND state = 'pending'", sessionID, promptRunID).
		Updates(map[string]any{
			"state": TurnRequestStateCancelled, "reason": nullableTrimmed(reason), "resolved_at": now,
		})
	if result.Error != nil {
		return fmt.Errorf("cancel pending Captain turn requests: %w", result.Error)
	}
	return nil
}

func nullableUUID(id uuid.UUID) *uuid.UUID {
	if id == uuid.Nil {
		return nil
	}
	return &id
}

func turnRequestFromRecord(record turnRequestRecord) TurnRequest {
	return TurnRequest{
		ID: record.ID, SessionID: record.SessionID, TurnID: record.TurnID, PromptRunID: record.PromptRunID, ModelCallID: record.ModelCallID,
		CredentialID: record.CredentialID, ToolCallID: optionalString(record.ToolCallID),
		Kind: record.Kind, State: record.State, Request: record.Request, Response: record.Response,
		IdempotencyKey: optionalString(record.IdempotencyKey), RequestedBy: optionalString(record.RequestedBy), ResolvedBy: optionalString(record.ResolvedBy),
		Reason: optionalString(record.Reason), Version: record.Version, ExpiresAt: record.ExpiresAt,
		CreatedAt: record.CreatedAt, ResolvedAt: record.ResolvedAt,
	}
}
