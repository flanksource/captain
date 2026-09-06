package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
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
	if request.Spec.Mode == "" || request.Spec.Provider == nil {
		return nil, fmt.Errorf("authoritative chat execution requires a resolved (provider, mode) runtime")
	}
	renderedSpec, err := renderedSpecMap(request.Spec, request.Profile)
	if err != nil {
		return nil, err
	}
	var session *database.Session
	var execution *databaseExecution
	var recovered *database.ChatTurn
	resumed := false
	err = a.db.Transaction(ctx, func(tx *database.DB) error {
		if !request.ExpectedThreadUpdatedAt.IsZero() {
			locked, lockErr := tx.LockSessionForUpdate(ctx, sessionID)
			if lockErr != nil {
				return lockErr
			}
			if !locked.UpdatedAt.Equal(request.ExpectedThreadUpdatedAt) {
				return fmt.Errorf("%w: chat thread %s changed after its history was read", database.ErrSessionConflict, sessionID)
			}
		}
		var createErr error
		session, createErr = tx.CreateOrGetSession(ctx, database.CreateSessionInput{
			ID: sessionID, Source: "aichat", Provider: request.Spec.Provider.Name,
			HostID: "local", Title: request.Title, InitialPrompt: initialUserPrompt(request.Spec),
			Metadata: map[string]any{"aichat": true},
		})
		if createErr != nil {
			return createErr
		}
		if session.Source != "aichat" {
			return fmt.Errorf("chat thread %s has incompatible source %q", request.ThreadID, session.Source)
		}
		recovered, createErr = tx.RecoverIncompleteChatAdmission(ctx, database.RecoverIncompleteChatAdmissionInput{
			SessionID: session.ID, ProviderTurnID: request.RequestID,
		})
		if createErr != nil {
			return createErr
		}
		turn, created, createErr := tx.CreateChatTurn(ctx, database.CreateChatTurnInput{
			SessionID: session.ID, ProviderTurnID: request.RequestID,
		})
		if createErr != nil {
			return createErr
		}
		if !created && turn.Status != database.TurnStatusOpen {
			return fmt.Errorf("chat turn %q already exists in state %s", request.RequestID, turn.Status)
		}
		resumed = !created
		run, createErr := tx.CreatePromptRun(ctx, database.CreatePromptRunInput{
			SessionID: session.ID, TurnID: &turn.ID, AdmissionKey: executionAdmissionKey(request),
			Origin: "aichat", RenderedSpec: renderedSpec,
			Runtime: database.PromptRunRuntime{
				// Runtime.Mode here is the RUN mode; the runtime mechanism travels on
				// the requested/resolved selections.
				Mode: "run", Driver: request.Spec.Provider.AgentName,
				Requested: runtimeSelection(request.Spec.Model),
				Resolved:  runtimeSelection(request.Spec.Model),
			},
			PromptMarkdown: initialUserPrompt(request.Spec),
		})
		if createErr != nil {
			return createErr
		}
		if run.State != database.PromptRunStatePending {
			return fmt.Errorf("chat request %q already has prompt run %s in state %s", request.RequestID, run.ID, run.State)
		}
		modelCallID, createErr := tx.CreateChatModelCall(ctx, database.CreateChatModelCallInput{
			TurnID: turn.ID, PromptRunID: run.ID, Model: request.Spec.Name,
			Provider: request.Spec.Provider.Name, Mode: string(request.Spec.Mode), Effort: string(request.Spec.Effort),
		})
		if createErr != nil {
			return createErr
		}
		execution = &databaseExecution{
			db: tx, ctx: ctx, session: session, turn: turn, run: run, modelCallID: modelCallID,
			model: request.Spec.Name, provider: request.Spec.Provider, mode: request.Spec.Mode,
			events: make(chan api.Event, 16), definitions: append([]api.ToolDefinition(nil), request.Definitions...),
			approvalIDs: map[string]uuid.UUID{}, providerToolUseReady: make(chan struct{}, 1),
		}
		return execution.markRunning(ctx)
	})
	if err != nil {
		return nil, err
	}
	execution.db = a.db
	if recovered != nil {
		serviceLog.Warnf("recovered incomplete chat admission turn %s for session %s", recovered.ID, session.ID)
	}
	if resumed {
		serviceLog.Warnf("resumed incomplete chat admission turn %s for session %s", execution.turn.ID, session.ID)
	}
	if len(request.Definitions) > 0 && request.Spec.Mode == api.ModeAgent {
		if err := execution.startCallerTools(ctx, request.Spec.Provider, request.Spec.Mode); err != nil {
			_ = execution.Close(context.Background())
			return nil, err
		}
	}
	return execution, nil
}

func (a *DatabaseExecutionAuthority) ResolveToolApproval(
	ctx context.Context,
	resolution ToolApprovalResolution,
) (*ApprovalContinuation, error) {
	var continuation *ApprovalContinuation
	err := a.db.Transaction(ctx, func(tx *database.DB) error {
		var resolveErr error
		continuation, resolveErr = (&DatabaseExecutionAuthority{db: tx}).resolveToolApproval(ctx, resolution)
		return resolveErr
	})
	if err != nil {
		return nil, err
	}
	if continuation != nil {
		continuation.Execution.(*databaseExecution).db = a.db
	}
	return continuation, nil
}

func (a *DatabaseExecutionAuthority) resolveToolApproval(
	ctx context.Context,
	resolution ToolApprovalResolution,
) (*ApprovalContinuation, error) {
	sessionID, err := uuid.Parse(resolution.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("chat thread ID %q is not a UUID: %w", resolution.ThreadID, err)
	}
	approvalID, err := uuid.Parse(resolution.ApprovalID)
	if err != nil {
		return nil, fmt.Errorf("tool approval ID %q is not a UUID: %w", resolution.ApprovalID, err)
	}
	var expectedTurnID *uuid.UUID
	if strings.TrimSpace(resolution.ExpectedTurnID) != "" {
		parsed, parseErr := uuid.Parse(resolution.ExpectedTurnID)
		if parseErr != nil {
			return nil, fmt.Errorf("active chat turn ID %q is not a UUID: %w", resolution.ExpectedTurnID, parseErr)
		}
		expectedTurnID = &parsed
	}
	request, err := a.db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
		SessionID: sessionID, RequestID: approvalID, ExpectedTurnID: expectedTurnID,
		Approved: resolution.Approved, UpdatedInput: resolution.UpdatedInput,
		ResolvedBy: "chat", Reason: resolution.Reason,
	})
	if err != nil {
		return nil, err
	}
	if request.CredentialID != nil {
		return nil, nil
	}
	if request.PromptRunID == nil || request.TurnID == nil {
		return nil, fmt.Errorf("provider approval %s has no prompt run or turn", request.ID)
	}
	requests, err := a.db.ListTurnRequests(ctx, database.TurnRequestFilter{
		SessionID: sessionID, PromptRunID: request.PromptRunID,
	})
	if err != nil {
		return nil, err
	}
	for _, item := range requests {
		if item.State == database.TurnRequestStatePending {
			return nil, nil
		}
	}
	run, err := a.db.GetPromptRun(ctx, *request.PromptRunID)
	if err != nil {
		return nil, err
	}
	if run.State != database.PromptRunStateWaiting {
		return nil, nil
	}
	if run.ApprovalState == nil || run.ProviderCheckpoint == nil {
		return nil, fmt.Errorf("waiting prompt run %s has no durable approval state and provider checkpoint", run.ID)
	}
	state := *run.ApprovalState
	state.ProviderCheckpoint = &api.ProviderCheckpoint{
		Codec: run.ProviderCheckpoint.Codec, Version: run.ProviderCheckpoint.Version,
		Payload: append([]byte(nil), run.ProviderCheckpoint.Payload...),
	}
	decisions, err := approvalDecisions(state, requests)
	if err != nil {
		return nil, err
	}
	renderedSpec := maps.Clone(run.RenderedSpec)
	delete(renderedSpec, "resolution")
	rendered, err := json.Marshal(renderedSpec)
	if err != nil {
		return nil, fmt.Errorf("encode prompt run %s rendered spec: %w", run.ID, err)
	}
	var spec api.Spec
	if err := json.Unmarshal(rendered, &spec); err != nil {
		return nil, fmt.Errorf("decode prompt run %s rendered spec: %w", run.ID, err)
	}
	// RenderedSpec cannot carry the resolved provider (api.Model.Provider is
	// json:"-"), so the runtime comes from the run's recorded selection rather than
	// the decoded spec. Re-deriving it from the model name would resume an agent
	// turn on the Anthropic API, and would miss a model a fallback swapped in
	// mid-turn.
	runtime := run.Runtime.Effective()
	if strings.TrimSpace(runtime.Mode) == "" || strings.TrimSpace(runtime.Model) == "" {
		return nil, fmt.Errorf("prompt run %s has no recorded runtime to resume: model %q mode %q",
			run.ID, runtime.Model, runtime.Mode)
	}
	spec.Model = runtimeModel(runtime, spec.Model)
	spec.Messages = nil
	spec.Prompt.User = ""
	spec.Prompt.System = ""
	spec.Prompt.AppendSystem = ""
	spec.Prompt.Attachments = nil
	spec.ToolApproval = &api.ToolApprovalResume{State: state, Decisions: decisions}
	turn, err := a.db.GetChatTurn(ctx, *request.TurnID)
	if err != nil {
		return nil, err
	}
	running := database.PromptRunStateRunning
	phase := database.PromptRunPhaseGenerate
	var resumed *database.PromptRun
	var modelCallID uuid.UUID
	resumed, err = a.db.UpdatePromptRun(ctx, database.UpdatePromptRunInput{
		ID: run.ID, ExpectedVersion: run.Version, State: &running, Phase: &phase,
		ClearApprovalState: true, ClearProviderCheckpoint: true,
	})
	if err != nil {
		return nil, err
	}
	modelCallID, err = a.db.CreateChatModelCall(ctx, database.CreateChatModelCallInput{
		TurnID: turn.ID, PromptRunID: run.ID, Model: spec.Name,
		Provider: providerKey(spec.Provider), Mode: string(spec.Mode), Effort: string(spec.Effort),
	})
	if err != nil {
		return nil, err
	}
	sessionRecord, err := a.db.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	execution := &databaseExecution{
		db: a.db, ctx: ctx, session: sessionRecord, turn: turn, run: resumed, modelCallID: modelCallID,
		model: spec.Name, provider: spec.Provider, mode: spec.Mode,
		events: make(chan api.Event, 16), approvalIDs: map[string]uuid.UUID{},
		providerToolUseReady: make(chan struct{}, 1),
	}
	if err := execution.updateSessionActivity(ctx, database.SessionActivityWorking); err != nil {
		return nil, err
	}
	return &ApprovalContinuation{Execution: execution, Spec: spec}, nil
}

func approvalDecisions(state api.ToolApprovalState, requests []database.TurnRequest) ([]api.ToolApprovalDecision, error) {
	byCall := make(map[string]database.TurnRequest, len(requests))
	for _, request := range requests {
		byCall[request.ToolCallID] = request
	}
	decisions := make([]api.ToolApprovalDecision, 0, len(state.Pending()))
	for _, pending := range state.Pending() {
		request, ok := byCall[pending.ToolCallID]
		if !ok {
			return nil, fmt.Errorf("approval state tool call %q has no durable turn request", pending.ToolCallID)
		}
		decision := api.ToolApprovalDecision{
			ApprovalID: request.ID.String(), ToolCallID: pending.ToolCallID, Tool: pending.Tool,
		}
		switch request.State {
		case database.TurnRequestStateApproved:
			decision.Action = api.ToolApprovalApprove
			if updated := request.Response["updatedInput"]; updated != nil {
				encoded, err := json.Marshal(updated)
				if err != nil {
					return nil, fmt.Errorf("encode approval %s updated input: %w", request.ID, err)
				}
				decision.Input = encoded
			}
		case database.TurnRequestStateDenied:
			decision.Action = api.ToolApprovalDeny
			decision.Message = request.Reason
		default:
			return nil, fmt.Errorf("approval %s is in non-resumable state %s", request.ID, request.State)
		}
		decisions = append(decisions, decision)
	}
	return decisions, nil
}

func runtimeSelection(model api.Model) database.PromptRunRuntimeSelection {
	identity := api.RuntimeIdentityOf(model)
	return database.PromptRunRuntimeSelection{
		Provider: identity.Provider, Mode: string(identity.Mode),
		Model: identity.Model, Effort: string(identity.Effort),
	}
}

// runtimeModel is the inverse of runtimeSelection: it restores the recorded model
// identity onto base, leaving the rest of the authored model (fallbacks,
// temperature, cache settings) as the stored spec declared it.
func runtimeModel(selection database.PromptRunRuntimeSelection, base api.Model) api.Model {
	base.Name = selection.Model
	base.Mode = api.RuntimeMode(selection.Mode)
	base.Effort = api.Effort(selection.Effort)
	if p, ok := api.ProviderByName(selection.Provider); ok {
		base.Provider = p
	}
	return base
}

func renderedSpecMap(spec api.Spec, profile api.ResolvedSpec) (map[string]any, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("encode authoritative chat spec: %w", err)
	}
	var rendered map[string]any
	if err := json.Unmarshal(raw, &rendered); err != nil {
		return nil, fmt.Errorf("decode authoritative chat spec: %w", err)
	}
	if len(profile.Trace) > 0 {
		resolution, err := json.Marshal(struct {
			Constraints api.RuntimeConstraints `json:"constraints"`
			Trace       []api.SpecLayer        `json:"trace"`
		}{Constraints: profile.Constraints, Trace: profile.Trace})
		if err != nil {
			return nil, fmt.Errorf("encode authoritative chat profile: %w", err)
		}
		var value map[string]any
		if err := json.Unmarshal(resolution, &value); err != nil {
			return nil, fmt.Errorf("decode authoritative chat profile: %w", err)
		}
		rendered["resolution"] = value
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
