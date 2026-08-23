package aichat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

const (
	callerToolApprovalTimeout = 5 * time.Minute
	providerApprovalTimeout   = 24 * time.Hour
	approvalPollInterval      = 100 * time.Millisecond
)

type databaseExecution struct {
	db          *database.DB
	ctx         context.Context
	session     *database.Session
	turn        *database.ChatTurn
	run         *database.PromptRun
	modelCallID uuid.UUID
	model       string
	backend     api.Backend
	definitions []api.ToolDefinition
	events      chan api.Event

	mu                   sync.Mutex
	finishMu             sync.Mutex
	credential           *database.CallerToolCredential
	runtime              *callertools.Runtime
	endpoint             *api.CallerToolEndpoint
	terminal             bool
	suspended            bool
	closed               bool
	providerID           string
	approvalIDs          map[string]uuid.UUID
	providerToolUses     []api.Event
	providerToolUseReady chan struct{}
}

// finishModelCall persists a terminal model call with its priced cost breakdown.
// Pricing happens here, against the same model identity the call was created
// with, so the five per-bucket cost columns and the provider-reported total are
// both stored rather than the whole figure collapsing into output_cost.
func (e *databaseExecution) finishModelCall(
	ctx context.Context,
	status database.ModelCallStatus,
	stopReason string,
	event api.Event,
) error {
	input := database.FinishChatModelCallInput{
		ID: e.modelCallID, Status: status, StopReason: stopReason, Event: event,
		ContextWindowTokens: ai.ContextWindowFor(e.backend, e.model),
	}
	if event.Usage != nil {
		cost := ai.PriceUsage(e.backend, e.model, *event.Usage, event.CostUSD)
		input.Cost = &cost
	}
	return e.db.FinishChatModelCall(ctx, input)
}

func (e *databaseExecution) CaptainSessionID() string { return e.session.ID.String() }
func (e *databaseExecution) TurnID() string           { return e.turn.ID.String() }
func (e *databaseExecution) PromptRunID() string      { return e.run.ID.String() }
func (e *databaseExecution) Events() <-chan api.Event { return e.events }

func (e *databaseExecution) CallerTools() *api.CallerToolEndpoint {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.endpoint == nil {
		return nil
	}
	endpoint := *e.endpoint
	endpoint.Headers = cloneStringValues(e.endpoint.Headers)
	return &endpoint
}

func (e *databaseExecution) startCallerTools(ctx context.Context, backend api.Backend) error {
	var credentialID uuid.UUID
	runtime, err := callertools.New(callertools.Options{
		Definitions: e.definitions, SessionID: e.session.ID.String(),
		ApprovalTimeout: callerToolApprovalTimeout,
		ValidateCredential: func(ctx context.Context) error {
			if credentialID == uuid.Nil {
				return fmt.Errorf("caller-tool credential has not been issued")
			}
			return e.db.ValidateCallerToolCredential(ctx, credentialID)
		},
		CanUseTool: func(ctx context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
			return e.requestApproval(ctx, credentialID, request)
		},
	})
	if err != nil {
		return err
	}
	policy := make(map[string]api.ToolPolicy, len(e.definitions))
	for _, definition := range e.definitions {
		policy[definition.Name] = definition.DefaultPermission
	}
	credential, err := e.db.CreateCallerToolCredential(ctx, database.CreateCallerToolCredentialInput{
		SessionID: e.session.ID, PromptRunID: e.run.ID, Backend: backend,
		SecretHash: runtime.CredentialHash(), Policy: policy,
	})
	if err != nil {
		_ = runtime.Close()
		return err
	}
	credentialID = credential.ID
	endpoint := runtime.Endpoint()
	e.mu.Lock()
	e.runtime = runtime
	e.credential = credential
	e.endpoint = &endpoint
	e.mu.Unlock()
	return nil
}

func (e *databaseExecution) requestApproval(
	ctx context.Context,
	credentialID uuid.UUID,
	request api.PermissionRequest,
) (api.PermissionDecision, error) {
	if request.ToolUseIDGenerated {
		toolUseID, err := e.claimProviderToolUse(ctx, request)
		if err != nil {
			return api.PermissionDecision{}, err
		}
		request.ToolUseID = toolUseID
	}
	expiresAt := time.Now().Add(callerToolApprovalTimeout)
	pending, err := e.db.CreateToolApprovalRequest(ctx, database.CreateToolApprovalRequestInput{
		CredentialID: credentialID, SessionID: e.session.ID, TurnID: e.turn.ID, PromptRunID: e.run.ID,
		ModelCallID: e.modelCallID, RequestedBy: "caller_tool",
		ToolCallID: request.ToolUseID, Tool: request.Tool, Input: request.Input,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return api.PermissionDecision{}, err
	}
	if err := e.markWaiting(ctx); err != nil {
		return api.PermissionDecision{}, err
	}
	if err := e.emitApproval(ctx, pending.ID, request); err != nil {
		return api.PermissionDecision{}, err
	}
	decision, err := e.waitForApproval(ctx, pending.ID)
	restoreErr := e.markRunning(ctx)
	return decision, errors.Join(err, restoreErr)
}

func (e *databaseExecution) emitApproval(ctx context.Context, approvalID uuid.UUID, request api.PermissionRequest) error {
	event := api.Event{
		Kind: api.EventPermission, Tool: request.Tool,
		ToolCallID: request.ToolUseID, ApprovalID: approvalID.String(), Input: request.Input,
	}
	select {
	case e.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *databaseExecution) waitForApproval(
	ctx context.Context,
	requestID uuid.UUID,
) (api.PermissionDecision, error) {
	ticker := time.NewTicker(approvalPollInterval)
	defer ticker.Stop()
	for {
		request, err := e.db.GetTurnRequest(ctx, requestID)
		if err != nil {
			return api.PermissionDecision{}, err
		}
		switch request.State {
		case database.TurnRequestStateApproved:
			decision := api.PermissionDecision{Allow: true}
			if updated, ok := request.Response["updatedInput"].(map[string]any); ok {
				decision.UpdatedInput = updated
			}
			return decision, nil
		case database.TurnRequestStateDenied:
			message := request.Reason
			if message == "" {
				message = "tool call denied"
			}
			return api.PermissionDecision{Message: message}, nil
		case database.TurnRequestStateExpired, database.TurnRequestStateCancelled:
			return api.PermissionDecision{}, fmt.Errorf("tool approval %s", request.State)
		}
		if request.ExpiresAt != nil && !time.Now().Before(*request.ExpiresAt) {
			if err := e.db.ExpireToolApprovalRequest(ctx, request.ID, database.TurnRequestStateExpired, "approval timed out"); err != nil {
				return api.PermissionDecision{}, err
			}
			continue
		}
		if err := e.db.ValidateCallerToolCredential(ctx, *request.CredentialID); err != nil {
			_ = e.db.ExpireToolApprovalRequest(ctx, request.ID, database.TurnRequestStateCancelled, err.Error())
			return api.PermissionDecision{}, err
		}
		select {
		case <-ctx.Done():
			_ = e.db.ExpireToolApprovalRequest(context.Background(), request.ID, database.TurnRequestStateCancelled, ctx.Err().Error())
			return api.PermissionDecision{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (e *databaseExecution) Observe(ctx context.Context, event api.Event) (api.Event, error) {
	if event.SessionID != "" {
		if err := e.bindProviderSession(ctx, event.SessionID); err != nil {
			return event, err
		}
	}
	switch event.Kind {
	case api.EventToolUse:
		e.rememberProviderToolUse(event)
		return event, nil
	case api.EventPermission:
		if event.ApprovalID != "" {
			return event, nil
		}
		approval, err := e.createProviderApproval(ctx, event)
		if err != nil {
			return event, err
		}
		event.ApprovalID = approval.ID.String()
		return event, nil
	case api.EventResult:
		if event.ToolApproval != nil {
			return event, e.suspend(ctx, *event.ToolApproval, event)
		}
		return event, e.finish(ctx, true, "", event)
	case api.EventError:
		return event, e.finish(ctx, false, event.Error, event)
	default:
		return event, nil
	}
}

func (e *databaseExecution) suspend(ctx context.Context, state api.ToolApprovalState, event api.Event) error {
	if state.ProviderCheckpoint == nil {
		return fmt.Errorf("provider tool approval ended without a private checkpoint")
	}
	for _, pending := range state.Pending() {
		e.mu.Lock()
		_, ok := e.approvalIDs[pending.ToolCallID]
		e.mu.Unlock()
		if !ok {
			return fmt.Errorf("provider tool approval %q has no durable turn request", pending.ToolCallID)
		}
	}
	if err := e.finishModelCall(ctx, database.ModelCallStatusSucceeded, "tool_approval", event); err != nil {
		return err
	}
	checkpoint := database.PromptRunCheckpoint{
		Codec: state.ProviderCheckpoint.Codec, Version: state.ProviderCheckpoint.Version,
		Payload: state.ProviderCheckpoint.Payload,
	}
	waiting := database.PromptRunStateWaiting
	state.ProviderCheckpoint = nil
	if err := e.updateRun(ctx, runUpdate{
		State: &waiting, ApprovalState: &state, ProviderCheckpoint: &checkpoint,
	}); err != nil {
		return err
	}
	if err := e.updateSessionActivity(ctx, database.SessionActivityApproval); err != nil {
		return err
	}
	e.mu.Lock()
	e.suspended = true
	e.mu.Unlock()
	return nil
}

func (e *databaseExecution) Close(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	terminal := e.terminal
	suspended := e.suspended
	runtime := e.runtime
	credential := e.credential
	e.mu.Unlock()

	var errs []error
	if !terminal && !suspended {
		errs = append(errs, e.finish(ctx, false, "provider stream ended without a terminal event", api.Event{Kind: api.EventError, Error: "provider stream ended without a terminal event"}))
	}
	if credential != nil {
		errs = append(errs, e.db.RevokeCallerToolCredential(ctx, credential.ID, "execution closed"))
	}
	if runtime != nil {
		errs = append(errs, runtime.Close())
	}
	return errors.Join(errs...)
}

func (e *databaseExecution) Interrupt(ctx context.Context, reason string) error {
	e.finishMu.Lock()
	defer e.finishMu.Unlock()
	e.mu.Lock()
	if e.terminal {
		e.mu.Unlock()
		return nil
	}
	runtime := e.runtime
	credential := e.credential
	e.mu.Unlock()

	if runtime != nil {
		runtime.Revoke()
	}
	var errs []error
	errs = append(errs, e.finishModelCall(ctx, database.ModelCallStatusCancelled, "interrupt",
		api.Event{Kind: api.EventInterrupted, Reason: reason}))
	errs = append(errs, e.db.CancelPendingTurnRequests(ctx, e.session.ID, e.run.ID, "execution interrupted"))
	if credential != nil {
		errs = append(errs, e.db.RevokeCallerToolCredential(ctx, credential.ID, "execution interrupted"))
	}
	phase := database.PromptRunPhaseFinished
	state := database.PromptRunStateCancelled
	errs = append(errs, e.updateRun(ctx, runUpdate{
		Phase: &phase, State: &state, ClearApprovalState: true, ClearProviderCheckpoint: true,
	}))
	errs = append(errs, e.db.FinishChatTurn(ctx, e.turn.ID, database.TurnStatusInterrupted, "interrupt"))
	errs = append(errs, e.updateSessionState(
		ctx, database.SessionLifecycleInterrupted, database.SessionActivityIdle, reason,
	))
	if err := errors.Join(errs...); err != nil {
		return err
	}
	e.mu.Lock()
	e.terminal = true
	e.mu.Unlock()
	return nil
}

func (e *databaseExecution) bindProviderSession(ctx context.Context, providerID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	providerID = strings.TrimSpace(providerID)
	if providerID == "" || providerID == e.providerID {
		return nil
	}
	if e.providerID != "" {
		return fmt.Errorf("provider session is already bound to %q", e.providerID)
	}
	session, err := e.db.GetSession(ctx, e.session.ID)
	if err != nil {
		return err
	}
	updated, err := e.db.UpdateSessionState(ctx, database.UpdateSessionStateInput{
		ID: session.ID, ExpectedVersion: session.StateVersion, ProviderSessionID: &providerID,
	})
	if err != nil {
		return err
	}
	e.session = updated
	e.providerID = providerID
	return nil
}

func (e *databaseExecution) markRunning(ctx context.Context) error {
	phase := database.PromptRunPhaseGenerate
	state := database.PromptRunStateRunning
	activity := database.SessionActivityWorking
	if err := e.updateRun(ctx, runUpdate{Phase: &phase, State: &state}); err != nil {
		return err
	}
	return e.updateSessionActivity(ctx, activity)
}

func (e *databaseExecution) markWaiting(ctx context.Context) error {
	state := database.PromptRunStateWaiting
	activity := database.SessionActivityApproval
	if err := e.updateRun(ctx, runUpdate{State: &state}); err != nil {
		return err
	}
	return e.updateSessionActivity(ctx, activity)
}

func (e *databaseExecution) finish(ctx context.Context, success bool, message string, event api.Event) error {
	e.finishMu.Lock()
	defer e.finishMu.Unlock()
	e.mu.Lock()
	if e.terminal {
		e.mu.Unlock()
		return nil
	}
	runtime := e.runtime
	credential := e.credential
	e.mu.Unlock()

	if runtime != nil {
		runtime.Revoke()
	}
	var errs []error
	callStatus := database.ModelCallStatusFailed
	stopReason := "error"
	if success {
		callStatus = database.ModelCallStatusSucceeded
		stopReason = "stop"
	}
	errs = append(errs, e.finishModelCall(ctx, callStatus, stopReason, event))
	if credential != nil {
		errs = append(errs, e.db.RevokeCallerToolCredential(ctx, credential.ID, "prompt run terminal"))
	}
	phase := database.PromptRunPhaseFinished
	state := database.PromptRunStateFailed
	if success {
		state = database.PromptRunStateSucceeded
	}
	errs = append(errs, e.updateRun(ctx, runUpdate{
		Phase: &phase, State: &state, Message: &message,
		ClearApprovalState: true, ClearProviderCheckpoint: true,
	}))
	turnState := database.TurnStatusError
	turnStopReason := message
	if success {
		turnState = database.TurnStatusEnded
		turnStopReason = "stop"
	}
	errs = append(errs, e.db.FinishChatTurn(ctx, e.turn.ID, turnState, turnStopReason))
	lifecycle := database.SessionLifecycleFailed
	if success {
		lifecycle = database.SessionLifecycleSucceeded
	}
	errs = append(errs, e.updateSessionState(ctx, lifecycle, database.SessionActivityIdle, message))
	if err := errors.Join(errs...); err != nil {
		return err
	}
	e.mu.Lock()
	e.terminal = true
	e.mu.Unlock()
	return nil
}

type runUpdate struct {
	Phase                   *database.PromptRunPhase
	State                   *database.PromptRunState
	Message                 *string
	ApprovalState           *api.ToolApprovalState
	ProviderCheckpoint      *database.PromptRunCheckpoint
	ClearApprovalState      bool
	ClearProviderCheckpoint bool
}

func (e *databaseExecution) updateRun(ctx context.Context, update runUpdate) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	input := database.UpdatePromptRunInput{
		ID: e.run.ID, ExpectedVersion: e.run.Version, Phase: update.Phase, State: update.State,
	}
	if update.Message != nil && *update.Message != "" {
		input.Error = update.Message
	}
	input.ApprovalState = update.ApprovalState
	input.ProviderCheckpoint = update.ProviderCheckpoint
	input.ClearApprovalState = update.ClearApprovalState
	input.ClearProviderCheckpoint = update.ClearProviderCheckpoint
	run, err := e.db.UpdatePromptRun(ctx, input)
	if err != nil {
		return err
	}
	e.run = run
	return nil
}

func (e *databaseExecution) updateSessionActivity(
	ctx context.Context,
	activity database.SessionActivityState,
) error {
	return e.updateSessionState(ctx, database.SessionLifecycleRunning, activity, "")
}

func (e *databaseExecution) updateSessionState(
	ctx context.Context,
	lifecycle database.SessionLifecycleStatus,
	activity database.SessionActivityState,
	reason string,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	session, err := e.db.GetSession(ctx, e.session.ID)
	if err != nil {
		return err
	}
	updated, err := e.db.UpdateSessionState(ctx, database.UpdateSessionStateInput{
		ID: session.ID, ExpectedVersion: session.StateVersion,
		LifecycleStatus: &lifecycle, ActivityState: &activity, StateReason: &reason,
	})
	if err != nil {
		return err
	}
	e.session = updated
	return nil
}
