package aichat

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

const terminalCommitTimeout = 10 * time.Second

func (e *databaseExecution) CommitTerminal(ctx context.Context, commit TerminalCommit) error {
	e.finishMu.Lock()
	defer e.finishMu.Unlock()
	if !isExecutionTerminalEvent(commit.Event) {
		return fmt.Errorf("terminal execution cannot commit %q event", commit.Event.Kind)
	}
	if len(commit.Message.Parts) == 0 {
		return fmt.Errorf("completed assistant turn has no message parts")
	}
	e.mu.Lock()
	if e.terminal {
		e.mu.Unlock()
		return nil
	}
	e.terminalCommitStarted = true
	session, run, runtime, credential := e.session, e.run, e.runtime, e.credential
	model, provider, providerID := e.model, e.provider, e.providerID
	e.mu.Unlock()
	if eventProviderID := strings.TrimSpace(commit.Event.SessionID); eventProviderID != "" {
		if providerID != "" && providerID != eventProviderID {
			return fmt.Errorf("provider session is already bound to %q", providerID)
		}
		providerID = eventProviderID
	}
	parts, err := json.Marshal(commit.Message.Parts)
	if err != nil {
		return fmt.Errorf("encode terminal assistant message: %w", err)
	}
	callStatus, runState, turnStatus, lifecycle, stopReason, message := terminalState(commit.Event)
	phase := database.PromptRunPhaseFinished
	activity := database.SessionActivityIdle
	modelCall := database.FinishChatModelCallInput{
		ID: e.modelCallID, Status: callStatus, StopReason: stopReason, Event: commit.Event,
		ContextWindowTokens: ai.ContextWindowFor(provider, model),
	}
	if commit.Event.Usage != nil {
		cost := ai.PriceUsage(provider, model, *commit.Event.Usage, commit.Event.CostUSD)
		modelCall.Cost = &cost
	}
	runUpdate := database.UpdatePromptRunInput{
		ID: run.ID, ExpectedVersion: run.Version, Phase: &phase, State: &runState,
		ClearApprovalState: true, ClearProviderCheckpoint: true,
	}
	if message != "" {
		runUpdate.Error = &message
	}
	input := database.CompleteChatExecutionInput{
		SessionID: session.ID, ProviderSessionID: providerID,
		TurnID: e.turn.ID, TurnStatus: turnStatus, TurnReason: stopReason,
		PromptRun: runUpdate, ModelCall: modelCall,
		Message: database.PutChatMessageInput{
			SessionID: session.ID, TurnID: e.turn.ID, ProviderMessageID: commit.Message.ID,
			Role: commit.Message.Role, Parts: parts, Replace: commit.Replace,
		},
		LifecycleStatus: lifecycle, ActivityState: activity, StateReason: message,
	}
	if credential != nil {
		input.CredentialID = &credential.ID
	}
	input.TranscriptSession = transcriptSessionInput(session, provider, providerID)
	durableContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalCommitTimeout)
	defer cancel()
	completed, err := e.db.CompleteChatExecution(durableContext, input)
	if err != nil {
		return err
	}
	if runtime != nil {
		runtime.Revoke()
	}
	e.mu.Lock()
	e.session, e.run, e.providerID, e.terminal = completed.Session, completed.Run, providerID, true
	e.mu.Unlock()
	return nil
}

func terminalState(event api.Event) (
	database.ModelCallStatus,
	database.PromptRunState,
	database.TurnStatus,
	database.SessionLifecycleStatus,
	string,
	string,
) {
	if event.Kind == api.EventResult && event.Success {
		return database.ModelCallStatusSucceeded, database.PromptRunStateSucceeded,
			database.TurnStatusEnded, database.SessionLifecycleSucceeded, "stop", ""
	}
	if event.Kind == api.EventInterrupted {
		message := strings.TrimSpace(event.Error)
		if message == "" {
			message = "interrupted"
		}
		return database.ModelCallStatusCancelled, database.PromptRunStateCancelled,
			database.TurnStatusInterrupted, database.SessionLifecycleInterrupted, "interrupt", message
	}
	message := strings.TrimSpace(event.Error)
	if message == "" {
		message = "provider returned an unsuccessful result"
	}
	return database.ModelCallStatusFailed, database.PromptRunStateFailed,
		database.TurnStatusError, database.SessionLifecycleFailed, message, message
}

// transcriptSessionInput links the provider's own transcript session to this
// one. Only the local transports write a transcript, and which one they write is
// a property of the family alone — every Claude mode leaves a `claude`
// transcript — so this reads the provider, not the mode.
func transcriptSessionInput(session *database.Session, provider *api.ModelProvider, providerID string) *database.CreateSessionInput {
	if session == nil || provider == nil || strings.TrimSpace(providerID) == "" {
		return nil
	}
	switch provider {
	case api.Anthropic, api.OpenAI:
	default:
		return nil
	}
	return &database.CreateSessionInput{
		ProviderSessionID: providerID, HostID: session.HostID, CWD: session.CWD,
		ParentSessionID: &session.ID, ParentRelation: database.SessionParentRelationTranscript,
		Source: provider.AgentName, Provider: provider.Name,
	}
}

func isExecutionTerminalEvent(event api.Event) bool {
	return (event.Kind == api.EventResult && event.ToolApproval == nil) ||
		event.Kind == api.EventError || event.Kind == api.EventInterrupted
}
