package aichat

import (
	"context"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

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
