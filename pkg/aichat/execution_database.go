package aichat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

type databaseExecution struct {
	db          *database.DB
	ctx         context.Context
	session     *database.Session
	turn        *database.ChatTurn
	run         *database.PromptRun
	modelCallID uuid.UUID
	model       string
	provider    *api.ModelProvider
	mode        api.RuntimeMode
	definitions []api.ToolDefinition
	events      chan api.Event

	mu                    sync.Mutex
	finishMu              sync.Mutex
	credential            *database.CallerToolCredential
	runtime               *callertools.Runtime
	endpoint              *api.CallerToolEndpoint
	terminal              bool
	terminalCommitStarted bool
	suspended             bool
	closed                bool
	providerID            string
	approvalIDs           map[string]uuid.UUID
	providerToolUses      []api.Event
	providerToolUseReady  chan struct{}
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
	e.mu.Lock()
	model, provider := e.model, e.provider
	e.mu.Unlock()
	input := database.FinishChatModelCallInput{
		ID: e.modelCallID, Status: status, StopReason: stopReason, Event: event,
		ContextWindowTokens: ai.ContextWindowFor(provider, model),
	}
	if event.Usage != nil {
		cost := ai.PriceUsage(provider, model, *event.Usage, event.CostUSD)
		input.Cost = &cost
	}
	return e.db.FinishChatModelCall(ctx, input)
}

// providerKey is a provider descriptor's stored key, or "" when the runtime
// resolved to no provider.
func providerKey(p *api.ModelProvider) string {
	if p == nil {
		return ""
	}
	return p.Name
}

func (e *databaseExecution) CaptainSessionID() string { return e.session.ID.String() }
func (e *databaseExecution) TurnID() string           { return e.turn.ID.String() }
func (e *databaseExecution) PromptRunID() string      { return e.run.ID.String() }
func (e *databaseExecution) Events() <-chan api.Event { return e.events }

// BindRuntime atomically records the selected provider candidate on the thread,
// prompt run, and model call before provider-specific events are observed.
func (e *databaseExecution) BindRuntime(ctx context.Context, runtime api.Model) error {
	identity, err := threadRuntimeIdentity(runtime)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	runID, runVersion, runRuntime := e.run.ID, e.run.Version, e.run.Runtime
	runRuntime.Resolved = runtimeSelection(api.Model{
		Name: identity.Model, Provider: identity.ToModel().Provider, Mode: identity.Mode, Effort: runtime.Effort,
	})
	var updatedRun *database.PromptRun
	err = e.db.Transaction(ctx, func(tx *database.DB) error {
		if bindErr := tx.SetSessionMetadataOnce(ctx, e.session.ID, threadRuntimeMetadataKey,
			runtimeSelection(identity.ToModel())); bindErr != nil {
			if errors.Is(bindErr, database.ErrSessionConflict) {
				return fmt.Errorf("%w: %v", ErrThreadRuntimeConflict, bindErr)
			}
			return bindErr
		}
		if updateErr := tx.UpdateChatModelCallRuntime(ctx, database.UpdateChatModelCallRuntimeInput{
			ID: e.modelCallID, Model: identity.Model, Provider: identity.Provider, Mode: string(identity.Mode), Effort: string(runtime.Effort),
		}); updateErr != nil {
			return updateErr
		}
		updatedRun, err = tx.UpdatePromptRun(ctx, database.UpdatePromptRunInput{
			ID: runID, ExpectedVersion: runVersion, Runtime: &runRuntime,
		})
		return err
	})
	if err != nil {
		return err
	}
	e.model = identity.Model
	e.provider = identity.ToModel().Provider
	e.mode = identity.Mode
	e.run = updatedRun
	return nil
}

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

func (e *databaseExecution) startCallerTools(ctx context.Context, provider *api.ModelProvider, mode api.RuntimeMode) error {
	var credentialID uuid.UUID
	runtime, err := callertools.New(callertools.Options{
		Definitions: e.definitions, SessionID: e.session.ID.String(),
		ApprovalTimeout: approval.CallerToolTimeout,
		ValidateCredential: func(ctx context.Context) error {
			if credentialID == uuid.Nil {
				return fmt.Errorf("caller-tool credential has not been issued")
			}
			return e.db.ValidateCallerToolCredential(ctx, credentialID)
		},
		CanUseTool: func(ctx context.Context, request api.PermissionRequest) (api.PermissionDecision, error) {
			return e.approvalBroker(credentialID).CanUseTool(ctx, request)
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
		SessionID: e.session.ID, PromptRunID: e.run.ID, Provider: providerKey(provider), Mode: mode,
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

// approvalBroker is this execution's durable tool-approval seam. The caller-tool
// path names its credential, turn and model call, so a host answers it from the
// same captain_turn_requests row a credential-less provider run writes.
func (e *databaseExecution) approvalBroker(credentialID uuid.UUID) *approval.Broker {
	// The broker outlives this call and keeps whatever it is handed, so it gets
	// copies rather than pointers into the execution: &e.turn.ID and
	// &e.modelCallID aim inside state this execution mutates under e.mu (the
	// prompt run is swapped wholesale on every runtime bind), which is a live
	// read of shared memory from whichever goroutine later records the approval.
	e.mu.Lock()
	turnID, modelCallID, runID := e.turn.ID, e.modelCallID, e.run.ID
	e.mu.Unlock()
	return &approval.Broker{
		DB: e.db, SessionID: e.session.ID, PromptRunID: runID,
		TurnID: &turnID, ModelCallID: &modelCallID, CredentialID: credentialID,
		RequestedBy: "caller_tool", Timeout: approval.CallerToolTimeout,
		Notify: e.emit, OnWaiting: e.markWaiting, OnRunning: e.markRunning,
		ClaimToolUseID: e.claimProviderToolUse,
	}
}

func (e *databaseExecution) emit(ctx context.Context, event api.Event) error {
	select {
	case e.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
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
		pending, err := e.createProviderApproval(ctx, event)
		if err != nil {
			return event, err
		}
		event.ApprovalID = pending.ID.String()
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
	terminalCommitStarted := e.terminalCommitStarted
	suspended := e.suspended
	runtime := e.runtime
	credential := e.credential
	e.mu.Unlock()

	var errs []error
	if !terminal && !terminalCommitStarted && !suspended {
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
	var updated *database.Session
	err = e.db.Transaction(ctx, func(tx *database.DB) error {
		updated, err = tx.UpdateSessionState(ctx, database.UpdateSessionStateInput{
			ID: session.ID, ExpectedVersion: session.StateVersion, ProviderSessionID: &providerID,
		})
		if err != nil {
			return err
		}
		if transcript := transcriptSessionInput(session, e.provider, providerID); transcript != nil {
			_, err = tx.CreateOrGetSession(ctx, *transcript)
		}
		return err
	})
	if err != nil {
		return err
	}
	e.session = updated
	e.providerID = providerID
	return nil
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
