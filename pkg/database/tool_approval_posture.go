package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// HoldPromptRunForApprovals moves a prompt run to waiting while at least one of
// its tool approvals is pending, and reports whether the row moved.
//
// ResolveToolApprovalRequest only accepts a credential-less approval while its
// run is waiting, so the approval broker holds the run as soon as it records
// one. A run that already ended cannot be held: the question it would carry is
// one nobody is left to act on, so that is a conflict rather than a no-op.
func (db *DB) HoldPromptRunForApprovals(ctx context.Context, promptRunID uuid.UUID) (*PromptRun, bool, error) {
	return db.settleApprovalPosture(ctx, promptRunID, func(state PromptRunState, pending int64) (*PromptRunState, error) {
		switch state {
		case PromptRunStateSucceeded, PromptRunStateFailed, PromptRunStateCancelled:
			return nil, fmt.Errorf("%w: prompt run %s already ended as %s; no tool approval can hold it",
				ErrPromptRunConflict, promptRunID, state)
		}
		if pending == 0 || state == PromptRunStateWaiting {
			return nil, nil
		}
		waiting := PromptRunStateWaiting
		return &waiting, nil
	})
}

// ReleasePromptRunFromApprovals moves a waiting prompt run back to running once
// none of its tool approvals is pending, and reports whether the row moved.
//
// Parallel tool calls raise one approval each on the same run, so whichever wait
// ends first must not release a run its siblings still hold. A run that is not
// waiting — already released by a sibling, or ended by a stop — is left alone:
// it is not held, and releasing it would resurrect a finished run.
func (db *DB) ReleasePromptRunFromApprovals(ctx context.Context, promptRunID uuid.UUID) (*PromptRun, bool, error) {
	return db.settleApprovalPosture(ctx, promptRunID, func(state PromptRunState, pending int64) (*PromptRunState, error) {
		if state != PromptRunStateWaiting || pending > 0 {
			return nil, nil
		}
		running := PromptRunStateRunning
		return &running, nil
	})
}

// settleApprovalPosture reads the run under a row lock, counts its pending
// approvals, and applies the state decide picks.
//
// The lock is what makes the count trustworthy. Holding and releasing both take
// it before counting, so a release cannot count zero while a sibling's hold is
// between recording its approval and moving the run — the interleaving a
// read-count-write without a lock leaves open, and the one that strands the
// sibling's approval on a running run nobody can resolve it against. Under the
// lock the run's version cannot move, so the optimistic update cannot conflict.
func (db *DB) settleApprovalPosture(
	ctx context.Context,
	promptRunID uuid.UUID,
	decide func(state PromptRunState, pending int64) (*PromptRunState, error),
) (*PromptRun, bool, error) {
	if promptRunID == uuid.Nil {
		return nil, false, fmt.Errorf("%w: prompt run ID is required", ErrTurnRequestInvalid)
	}
	var settled *PromptRun
	var moved bool
	err := db.Transaction(ctx, func(tx *DB) error {
		var locked struct {
			State   PromptRunState
			Version int64
		}
		result := tx.gorm.WithContext(ctx).
			Raw(`SELECT state, version FROM captain_prompt_runs WHERE id = ? FOR UPDATE`, promptRunID).
			Scan(&locked)
		if result.Error != nil {
			return fmt.Errorf("lock Captain prompt run %s: %w", promptRunID, result.Error)
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("%w: %s", ErrPromptRunNotFound, promptRunID)
		}
		pending, err := tx.CountPendingToolApprovals(ctx, promptRunID)
		if err != nil {
			return err
		}
		target, err := decide(locked.State, pending)
		if err != nil {
			return err
		}
		if target == nil {
			settled, err = tx.GetPromptRun(ctx, promptRunID)
			return err
		}
		settled, err = tx.UpdatePromptRun(ctx, UpdatePromptRunInput{
			ID: promptRunID, ExpectedVersion: locked.Version, State: target,
		})
		moved = err == nil
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return settled, moved, nil
}
