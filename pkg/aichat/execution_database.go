package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
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
	approvalPollInterval      = 100 * time.Millisecond
)

type DatabaseExecutionAuthority struct {
	db *database.DB
}

func NewDatabaseExecutionAuthority(db *database.DB) (*DatabaseExecutionAuthority, error) {
	if db == nil || db.Gorm() == nil {
		return nil, fmt.Errorf("captain execution authority requires a database")
	}
	return &DatabaseExecutionAuthority{db: db}, nil
}

func (a *DatabaseExecutionAuthority) Begin(
	ctx context.Context,
	request ExecutionRequest,
) (Execution, error) {
	sessionID, err := uuid.Parse(request.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("chat thread ID %q is not a UUID: %w", request.ThreadID, err)
	}
	if request.Spec.Backend == "" {
		return nil, fmt.Errorf("authoritative chat execution requires a resolved backend")
	}
	fingerprint := runtimeFingerprint(request.Spec.Model)
	session, err := a.db.CreateOrGetSession(ctx, database.CreateSessionInput{
		ID: sessionID, Source: "aichat", Provider: ai.BackendToProvider(request.Spec.Backend),
		HostID: "local", Title: request.Title, InitialPrompt: initialUserPrompt(request.Spec),
		Metadata: map[string]any{"aichatRuntime": fingerprint},
	})
	if err != nil {
		return nil, err
	}
	if session.Source != "aichat" || !reflect.DeepEqual(session.Metadata["aichatRuntime"], fingerprint) {
		return nil, fmt.Errorf("chat thread %s has incompatible immutable runtime identity", request.ThreadID)
	}
	renderedSpec, err := renderedSpecMap(request.Spec)
	if err != nil {
		return nil, err
	}
	run, err := a.db.CreatePromptRun(ctx, database.CreatePromptRunInput{
		SessionID: session.ID, AdmissionKey: executionAdmissionKey(request),
		Origin: "aichat", RenderedSpec: renderedSpec,
		Runtime: database.PromptRunRuntime{
			Mode: string(request.Spec.Mode), Driver: string(request.Spec.Backend),
			Requested: runtimeSelection(request.Spec.Model),
			Resolved:  runtimeSelection(request.Spec.Model),
		},
		PromptMarkdown: initialUserPrompt(request.Spec),
	})
	if err != nil {
		return nil, err
	}
	if run.State != database.PromptRunStatePending {
		return nil, fmt.Errorf("chat request %q already has prompt run %s in state %s", request.RequestID, run.ID, run.State)
	}
	execution := &databaseExecution{
		db: a.db, ctx: ctx, session: session, run: run,
		events: make(chan api.Event, 16), definitions: append([]api.ToolDefinition(nil), request.Definitions...),
	}
	if err := execution.markRunning(ctx); err != nil {
		return nil, err
	}
	if len(request.Definitions) > 0 && isAgentBackend(request.Spec.Backend) {
		if err := execution.startCallerTools(ctx, request.Spec.Backend); err != nil {
			_ = execution.Close(context.Background())
			return nil, err
		}
	}
	return execution, nil
}

func (a *DatabaseExecutionAuthority) ResolveToolApproval(
	ctx context.Context,
	resolution ToolApprovalResolution,
) error {
	sessionID, err := uuid.Parse(resolution.ThreadID)
	if err != nil {
		return fmt.Errorf("chat thread ID %q is not a UUID: %w", resolution.ThreadID, err)
	}
	_, err = a.db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
		SessionID: sessionID, ToolCallID: resolution.ToolCallID,
		Approved: resolution.Approved, UpdatedInput: resolution.UpdatedInput,
		ResolvedBy: "chat", Reason: resolution.Reason,
	})
	return err
}

type databaseExecution struct {
	db          *database.DB
	ctx         context.Context
	session     *database.Session
	run         *database.PromptRun
	definitions []api.ToolDefinition
	events      chan api.Event

	mu         sync.Mutex
	finishMu   sync.Mutex
	credential *database.CallerToolCredential
	runtime    *callertools.Runtime
	endpoint   *api.CallerToolEndpoint
	terminal   bool
	closed     bool
	providerID string
}

func (e *databaseExecution) CaptainSessionID() string { return e.session.ID.String() }
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
	policy := make(map[string]api.ToolMode, len(e.definitions))
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
	expiresAt := time.Now().Add(callerToolApprovalTimeout)
	pending, err := e.db.CreateToolApprovalRequest(ctx, database.CreateToolApprovalRequestInput{
		CredentialID: credentialID, SessionID: e.session.ID, PromptRunID: e.run.ID,
		ToolCallID: request.ToolUseID, Tool: request.Tool, Input: request.Input,
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return api.PermissionDecision{}, err
	}
	if err := e.markWaiting(ctx); err != nil {
		return api.PermissionDecision{}, err
	}
	if err := e.emitApproval(ctx, request); err != nil {
		return api.PermissionDecision{}, err
	}
	decision, err := e.waitForApproval(ctx, pending.ID)
	restoreErr := e.markRunning(ctx)
	return decision, errors.Join(err, restoreErr)
}

func (e *databaseExecution) emitApproval(ctx context.Context, request api.PermissionRequest) error {
	event := api.Event{
		Kind: api.EventPermission, Tool: request.Tool,
		ToolCallID: request.ToolUseID, Input: request.Input,
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

func (e *databaseExecution) Observe(ctx context.Context, event api.Event) error {
	if event.SessionID != "" {
		if err := e.bindProviderSession(ctx, event.SessionID); err != nil {
			return err
		}
	}
	switch event.Kind {
	case api.EventResult:
		return e.finish(ctx, true, "")
	case api.EventError:
		return e.finish(ctx, false, event.Error)
	default:
		return nil
	}
}

func (e *databaseExecution) Close(ctx context.Context) error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	terminal := e.terminal
	runtime := e.runtime
	credential := e.credential
	e.mu.Unlock()

	var errs []error
	if !terminal {
		errs = append(errs, e.finish(ctx, false, "provider stream ended without a terminal event"))
	}
	if credential != nil {
		errs = append(errs, e.db.RevokeCallerToolCredential(ctx, credential.ID, "execution closed"))
	}
	if runtime != nil {
		errs = append(errs, runtime.Close())
	}
	return errors.Join(errs...)
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
	if err := e.updateRun(ctx, &phase, &state, nil); err != nil {
		return err
	}
	return e.updateSessionActivity(ctx, activity)
}

func (e *databaseExecution) markWaiting(ctx context.Context) error {
	state := database.PromptRunStateWaiting
	activity := database.SessionActivityApproval
	if err := e.updateRun(ctx, nil, &state, nil); err != nil {
		return err
	}
	return e.updateSessionActivity(ctx, activity)
}

func (e *databaseExecution) finish(ctx context.Context, success bool, message string) error {
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
	if credential != nil {
		errs = append(errs, e.db.RevokeCallerToolCredential(ctx, credential.ID, "prompt run terminal"))
	}
	phase := database.PromptRunPhaseFinished
	state := database.PromptRunStateFailed
	if success {
		state = database.PromptRunStateSucceeded
	}
	errs = append(errs, e.updateRun(ctx, &phase, &state, &message))
	errs = append(errs, e.updateSessionActivity(ctx, database.SessionActivityIdle))
	if err := errors.Join(errs...); err != nil {
		return err
	}
	e.mu.Lock()
	e.terminal = true
	e.mu.Unlock()
	return nil
}

func (e *databaseExecution) updateRun(
	ctx context.Context,
	phase *database.PromptRunPhase,
	state *database.PromptRunState,
	message *string,
) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	input := database.UpdatePromptRunInput{
		ID: e.run.ID, ExpectedVersion: e.run.Version, Phase: phase, State: state,
	}
	if message != nil && *message != "" {
		input.Error = message
	}
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
	e.mu.Lock()
	defer e.mu.Unlock()
	session, err := e.db.GetSession(ctx, e.session.ID)
	if err != nil {
		return err
	}
	lifecycle := database.SessionLifecycleRunning
	updated, err := e.db.UpdateSessionState(ctx, database.UpdateSessionStateInput{
		ID: session.ID, ExpectedVersion: session.StateVersion,
		LifecycleStatus: &lifecycle, ActivityState: &activity,
	})
	if err != nil {
		return err
	}
	e.session = updated
	return nil
}

func runtimeFingerprint(model api.Model) map[string]any {
	return map[string]any{
		"backend": string(model.Backend), "mode": string(model.Mode), "model": model.Name,
	}
}

func runtimeSelection(model api.Model) database.PromptRunRuntimeSelection {
	return database.PromptRunRuntimeSelection{
		Provider: ai.BackendToProvider(model.Backend), Backend: string(model.Backend),
		Model: model.Name, Effort: string(model.Effort),
	}
}

func renderedSpecMap(spec api.Spec) (map[string]any, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encode authoritative chat spec: %w", err)
	}
	var rendered map[string]any
	if err := json.Unmarshal(raw, &rendered); err != nil {
		return nil, fmt.Errorf("decode authoritative chat spec: %w", err)
	}
	return rendered, nil
}

func executionAdmissionKey(request ExecutionRequest) string {
	if strings.TrimSpace(request.RequestID) == "" {
		return ""
	}
	return "aichat:" + request.ThreadID + ":" + request.RequestID
}

func initialUserPrompt(spec api.Spec) string {
	if spec.Prompt.User != "" {
		return spec.Prompt.User
	}
	for i := len(spec.Messages) - 1; i >= 0; i-- {
		if spec.Messages[i].Role != api.RoleUser {
			continue
		}
		for _, part := range spec.Messages[i].Parts {
			if part.Type == api.PartText && strings.TrimSpace(part.Text) != "" {
				return part.Text
			}
		}
	}
	return ""
}

func cloneStringValues(values map[string]string) map[string]string {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
