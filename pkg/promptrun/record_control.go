package promptrun

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flanksource/captain/pkg/ai/approval"
	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
)

// ErrRunFinished reports a run that already reached a terminal state, which
// nothing may settle a second time.
var ErrRunFinished = errors.New("prompt run already finished")

// Fail marks a run failed from outside Run: a failure before dispatch, or a
// dispatcher that can no longer finish the run it admitted.
func Fail(ctx context.Context, db *database.DB, runID uuid.UUID, reason string) (*database.PromptRun, error) {
	return Settle(ctx, db, runID, Outcome{State: database.PromptRunStateFailed, Error: reason})
}

// Cancel marks a run cancelled — stopped by a person, or reclaimed from a
// dispatcher that is gone — and cancels its pending tool approvals with it.
func Cancel(ctx context.Context, db *database.DB, runID uuid.UUID, reason string) (*database.PromptRun, error) {
	return Settle(ctx, db, runID, Outcome{State: database.PromptRunStateCancelled, Error: reason})
}

// Settle files the outcome of a run that ended outside Run: a parked run whose
// session was continued from Captain's session page, a failure before
// dispatch, a stop. It files succeeded, waiting (the run parked again on a new
// envelope), failed and cancelled; a failure or cancellation names its reason
// in Error. The outcome replaces the run's result — text, structured output and
// error are written as given — and an empty Phase keeps the run's phase, with a
// run that never started promoted to generate.
//
// A run settled into a terminal state takes its pending tool approvals with it.
// Approvals outlive the process that raised them: left pending, a dashboard
// would still offer to answer them, and an answer would unblock a broker no
// longer there to hear it. The terminal state lands first, so the brokers
// cancelling wakes leave a finished run alone instead of passing it back
// through running.
func Settle(ctx context.Context, db *database.DB, runID uuid.UUID, outcome Outcome) (*database.PromptRun, error) {
	if db == nil {
		return nil, errors.New("promptrun: settling a run requires a database")
	}
	outcome.Error = strings.TrimSpace(outcome.Error)
	if err := validateSettlement(runID, outcome); err != nil {
		return nil, err
	}
	var refused error
	settled, err := updateRun(ctx, db, runID, func(run *database.PromptRun) (database.UpdatePromptRunInput, bool) {
		if finishedRun(run.State) {
			refused = fmt.Errorf("%w: run %s is %s", ErrRunFinished, run.ID, run.State)
			return database.UpdatePromptRunInput{}, false
		}
		return settlementUpdate(run, outcome), true
	})
	if refused != nil {
		return nil, refused
	}
	if err != nil {
		return nil, fmt.Errorf("promptrun: settle run %s as %s: %w", runID, outcome.State, err)
	}
	if !finishedRun(settled.State) {
		return settled, nil
	}
	reason := firstNonEmpty(outcome.Error, fmt.Sprintf("prompt run %s", outcome.State))
	if err := approval.CancelPending(ctx, db, settled.SessionID, settled.ID, reason); err != nil {
		return settled, fmt.Errorf("promptrun: cancel pending approvals of run %s: %w", settled.ID, err)
	}
	return settled, nil
}

func validateSettlement(runID uuid.UUID, outcome Outcome) error {
	switch outcome.State {
	case database.PromptRunStateSucceeded, database.PromptRunStateWaiting:
		return nil
	case database.PromptRunStateFailed, database.PromptRunStateCancelled:
		if outcome.Error == "" {
			return fmt.Errorf("promptrun: settling run %s as %s requires a reason", runID, outcome.State)
		}
		return nil
	}
	return fmt.Errorf("promptrun: cannot settle run %s as %q: an outcome is succeeded, waiting, failed or cancelled", runID, outcome.State)
}

func settlementUpdate(run *database.PromptRun, outcome Outcome) database.UpdatePromptRunInput {
	phase := outcome.Phase
	if phase == "" {
		phase = run.Phase
	}
	if phase == database.PromptRunPhaseQueued || phase == database.PromptRunPhasePreRun {
		phase = database.PromptRunPhaseGenerate
	}
	return database.UpdatePromptRunInput{
		State: &outcome.State, Phase: &phase,
		ResultText: &outcome.Text, ResultJSON: &outcome.JSON, Error: &outcome.Error,
	}
}
