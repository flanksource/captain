package aichat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
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
	if request.Spec.Backend == "" {
		return nil, fmt.Errorf("authoritative chat execution requires a resolved backend")
	}
	session, err := a.db.CreateOrGetSession(ctx, database.CreateSessionInput{
		ID: sessionID, Source: "aichat", Provider: ai.BackendToProvider(request.Spec.Backend),
		HostID: "local", Title: request.Title, InitialPrompt: initialUserPrompt(request.Spec),
		Metadata: map[string]any{"aichat": true},
	})
	if err != nil {
		return nil, err
	}
	if session.Source != "aichat" {
		return nil, fmt.Errorf("chat thread %s has incompatible source %q", request.ThreadID, session.Source)
	}
	turn, created, err := a.db.CreateChatTurn(ctx, database.CreateChatTurnInput{
		SessionID: session.ID, ProviderTurnID: request.RequestID,
	})
	if err != nil {
		return nil, err
	}
	if !created {
		return nil, fmt.Errorf("chat turn %q already exists in state %s", request.RequestID, turn.Status)
	}
	renderedSpec, err := renderedSpecMap(request.Spec)
	if err != nil {
		return nil, err
	}
	run, err := a.db.CreatePromptRun(ctx, database.CreatePromptRunInput{
		SessionID: session.ID, TurnID: &turn.ID, AdmissionKey: executionAdmissionKey(request),
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
	modelCallID, err := a.db.CreateChatModelCall(ctx, database.CreateChatModelCallInput{
		TurnID: turn.ID, PromptRunID: run.ID, Model: request.Spec.Name,
		Backend: string(request.Spec.Backend), Effort: string(request.Spec.Effort),
	})
	if err != nil {
		return nil, err
	}
	execution := &databaseExecution{
		db: a.db, ctx: ctx, session: session, turn: turn, run: run, modelCallID: modelCallID,
		model: request.Spec.Name, backend: request.Spec.Backend,
		events: make(chan api.Event, 16), definitions: append([]api.ToolDefinition(nil), request.Definitions...),
		approvalIDs: map[string]uuid.UUID{}, providerToolUseReady: make(chan struct{}, 1),
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
) (*ApprovalContinuation, error) {
	sessionID, err := uuid.Parse(resolution.ThreadID)
	if err != nil {
		return nil, fmt.Errorf("chat thread ID %q is not a UUID: %w", resolution.ThreadID, err)
	}
	approvalID, err := uuid.Parse(resolution.ApprovalID)
	if err != nil {
		return nil, fmt.Errorf("tool approval ID %q is not a UUID: %w", resolution.ApprovalID, err)
	}
	request, err := a.db.ResolveToolApprovalRequest(ctx, database.ResolveToolApprovalRequestInput{
		SessionID: sessionID, RequestID: approvalID,
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
	rendered, err := json.Marshal(run.RenderedSpec)
	if err != nil {
		return nil, fmt.Errorf("encode prompt run %s rendered spec: %w", run.ID, err)
	}
	var spec api.Spec
	if err := json.Unmarshal(rendered, &spec); err != nil {
		return nil, fmt.Errorf("decode prompt run %s rendered spec: %w", run.ID, err)
	}
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
	err = a.db.Transaction(ctx, func(tx *database.DB) error {
		var updateErr error
		resumed, updateErr = tx.UpdatePromptRun(ctx, database.UpdatePromptRunInput{
			ID: run.ID, ExpectedVersion: run.Version, State: &running, Phase: &phase,
			ClearApprovalState: true, ClearProviderCheckpoint: true,
		})
		if updateErr != nil {
			return updateErr
		}
		modelCallID, updateErr = tx.CreateChatModelCall(ctx, database.CreateChatModelCallInput{
			TurnID: turn.ID, PromptRunID: run.ID, Model: spec.Name,
			Backend: string(spec.Backend), Effort: string(spec.Effort),
		})
		return updateErr
	})
	if err != nil {
		if errors.Is(err, database.ErrPromptRunConflict) {
			return nil, nil
		}
		return nil, err
	}
	sessionRecord, err := a.db.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	execution := &databaseExecution{
		db: a.db, ctx: ctx, session: sessionRecord, turn: turn, run: resumed, modelCallID: modelCallID,
		model: spec.Name, backend: spec.Backend,
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
