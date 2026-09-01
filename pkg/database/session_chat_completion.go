package database

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

type CompleteChatExecutionInput struct {
	SessionID         uuid.UUID
	ProviderSessionID string
	TranscriptSession *CreateSessionInput
	TurnID            uuid.UUID
	TurnStatus        TurnStatus
	TurnReason        string
	PromptRun         UpdatePromptRunInput
	ModelCall         FinishChatModelCallInput
	Message           PutChatMessageInput
	CredentialID      *uuid.UUID
	LifecycleStatus   SessionLifecycleStatus
	ActivityState     SessionActivityState
	StateReason       string
}

type CompleteChatExecutionResult struct {
	Session *Session
	Run     *PromptRun
}

func (db *DB) CompleteChatExecution(ctx context.Context, input CompleteChatExecutionInput) (*CompleteChatExecutionResult, error) {
	if input.SessionID == uuid.Nil || input.TurnID == uuid.Nil || input.PromptRun.ID == uuid.Nil || input.ModelCall.ID == uuid.Nil {
		return nil, fmt.Errorf("%w: session, turn, prompt run, and model call are required", ErrInvalidIngest)
	}
	if input.Message.SessionID != input.SessionID || input.Message.TurnID != input.TurnID {
		return nil, fmt.Errorf("%w: terminal message identity does not match its execution", ErrInvalidIngest)
	}
	providerSessionID := strings.TrimSpace(input.ProviderSessionID)
	var completed CompleteChatExecutionResult
	err := db.Transaction(ctx, func(tx *DB) error {
		if err := tx.PutChatMessage(ctx, input.Message); err != nil {
			return fmt.Errorf("commit terminal assistant message: %w", err)
		}
		if err := tx.FinishChatModelCall(ctx, input.ModelCall); err != nil {
			return err
		}
		if input.CredentialID != nil {
			if err := tx.RevokeCallerToolCredential(ctx, *input.CredentialID, "prompt run terminal"); err != nil {
				return err
			}
		}
		run, err := tx.UpdatePromptRun(ctx, input.PromptRun)
		if err != nil {
			return err
		}
		if err := tx.FinishChatTurn(ctx, input.TurnID, input.TurnStatus, input.TurnReason); err != nil {
			return err
		}
		session, err := tx.GetSession(ctx, input.SessionID)
		if err != nil {
			return err
		}
		update := UpdateSessionStateInput{
			ID: session.ID, ExpectedVersion: session.StateVersion,
			LifecycleStatus: &input.LifecycleStatus, ActivityState: &input.ActivityState,
			StateReason: &input.StateReason,
		}
		if providerSessionID != "" {
			update.ProviderSessionID = &providerSessionID
		}
		session, err = tx.UpdateSessionState(ctx, update)
		if err != nil {
			return err
		}
		if input.TranscriptSession != nil {
			if _, err := tx.CreateOrGetSession(ctx, *input.TranscriptSession); err != nil {
				return fmt.Errorf("bind provider transcript session: %w", err)
			}
		}
		completed = CompleteChatExecutionResult{Session: session, Run: run}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &completed, nil
}
