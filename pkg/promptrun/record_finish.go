package promptrun

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
)

// Outcome is how a finished run is filed.
type Outcome struct {
	State database.PromptRunState
	// Phase is where the run ended. Empty means the phase it was in when it
	// ended, with a run that never started promoted to its first working phase.
	Phase database.PromptRunPhase
	Text  string
	JSON  map[string]any
	Error string
}

// DefaultOutcome classifies a finished run the way Captain reads it:
//
//   - stopped (its context ended: the caller cancelled it or its deadline fired)
//     is cancelled — its last turn was cut off, not judged;
//   - any other error is failed, in the phase it failed in;
//   - a run that ended by asking the person a question is waiting;
//   - a failing verdict is failed in the verify phase, with the verdict's reason;
//   - anything else succeeded.
//
// The result text and structured output are kept in every case. Structured
// output that is not a JSON object is an error, and the run is failed with it.
func DefaultOutcome(result Result, runErr error, stopped bool) (Outcome, error) {
	var out Outcome
	structured, err := structuredObject(result.StructuredData)
	if err != nil {
		return Outcome{State: database.PromptRunStateFailed, Error: err.Error()}, err
	}
	out.JSON = structured
	if out.Text, err = resultText(result.Response, structured); err != nil {
		return Outcome{State: database.PromptRunStateFailed, Error: err.Error()}, err
	}
	switch {
	case stopped:
		out.State, out.Phase = database.PromptRunStateCancelled, database.PromptRunPhaseFinished
		out.Error = errorText(runErr, "stopped")
	case runErr != nil:
		out.State, out.Error = database.PromptRunStateFailed, runErr.Error()
	case asksQuestions(result.TerminalOutcome):
		out.State, out.Phase = database.PromptRunStateWaiting, database.PromptRunPhaseGenerate
	case !result.Passed:
		out.State, out.Phase = database.PromptRunStateFailed, database.PromptRunPhaseVerify
		out.Error = FailureReason(result.Verdicts)
	default:
		out.State, out.Phase = database.PromptRunStateSucceeded, database.PromptRunPhaseFinished
	}
	return out, nil
}

func asksQuestions(outcome *api.TerminalOutcome) bool {
	return outcome != nil && outcome.Kind == api.TerminalOutcomeQuestions
}

func errorText(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	return err.Error()
}

func structuredObject(value any) (map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	if object, ok := value.(map[string]any); ok {
		return object, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("promptrun: encode structured output: %w", err)
	}
	if string(raw) == "null" {
		return nil, nil
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, fmt.Errorf("promptrun: structured output is not a JSON object: %w", err)
	}
	return object, nil
}

// resultText is the run's answer as text: what the model said, or its
// structured output when it said nothing else.
func resultText(response *api.Response, structured map[string]any) (string, error) {
	if response != nil && response.Text != "" {
		return response.Text, nil
	}
	if structured == nil {
		return "", nil
	}
	raw, err := json.Marshal(structured)
	if err != nil {
		return "", fmt.Errorf("promptrun: encode structured output text: %w", err)
	}
	return string(raw), nil
}

// complete finishes a run Run executed: classify it, file it, and hand back the
// result stamped with the run's id. Store errors join the run's own.
func (r *recorder) complete(classify func(Result, error, bool) (Outcome, error), result Result, runErr error, stopped bool) (Result, error) {
	result.PromptRunID = r.runID
	if classify == nil {
		classify = DefaultOutcome
	}
	outcome, classifyErr := classify(result, runErr, stopped)
	var notices []api.Notice
	if result.Response != nil && result.Response.Workspace != nil {
		notices = result.Response.Workspace.Notices
	}
	settleErr := r.settle(outcome, IterationRecords(result, stopped), notices)
	return result, errors.Join(runErr, classifyErr, settleErr)
}

// settle writes everything a finished run leaves behind: every iteration (one
// refused row costs that row, not the others), the notices on the transcript
// the run bound, and the run's terminal state. A run something else already
// finished — a Cancel that raced the run's own end — keeps the state it has.
func (r *recorder) settle(outcome Outcome, iterations []database.UpsertPromptRunIterationInput, notices []api.Notice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	errs := []error{r.firstWriteErr}
	for _, record := range iterations {
		record.PromptRunID = r.runID
		if _, err := r.db.UpsertPromptRunIteration(r.ctx, record); err != nil {
			errs = append(errs, fmt.Errorf("promptrun: record iteration %d of run %s: %w", record.Iteration, r.runID, err))
		}
	}
	errs = append(errs, r.writeTranscript(notices))
	_, err := r.updateRun(func(run *database.PromptRun) (database.UpdatePromptRunInput, bool) {
		return r.finishUpdate(run, outcome), !finishedRun(run.State)
	})
	if err != nil {
		errs = append(errs, fmt.Errorf("promptrun: finish run %s: %w", r.runID, err))
	}
	return errors.Join(errs...)
}

func (r *recorder) finishUpdate(run *database.PromptRun, outcome Outcome) database.UpdatePromptRunInput {
	phase := outcome.Phase
	if phase == "" {
		phase = r.settledPhase(run.Phase)
	}
	runtime := r.resolvedRuntime(run.Runtime, &api.Spec{})
	update := database.UpdatePromptRunInput{State: &outcome.State, Phase: &phase, Runtime: &runtime}
	if outcome.Text != "" {
		update.ResultText = &outcome.Text
	}
	if outcome.Error != "" {
		update.Error = &outcome.Error
	}
	if outcome.JSON != nil {
		update.ResultJSON = &outcome.JSON
	}
	return update
}

// settledPhase is the phase a run ended in when its outcome names none: the one
// it was in, or — for a run that never started — its first working phase.
func (r *recorder) settledPhase(current database.PromptRunPhase) database.PromptRunPhase {
	if current == database.PromptRunPhaseQueued || current == database.PromptRunPhasePreRun {
		return r.activePhase()
	}
	return current
}

// writeTranscript finishes the transcript session the run bound: it re-arms the
// monitor on the log, which a finished agent has certainly flushed even when it
// had not at start, and records the run's lifecycle notices on it — the row the
// dashboard streams messages from. A run that bound no transcript (the API
// mode, a verify-only run) has nowhere to put them; its verdicts are on its
// iterations instead.
func (r *recorder) writeTranscript(notices []api.Notice) error {
	run, err := r.db.GetPromptRun(r.ctx, r.runID)
	if err != nil {
		return err
	}
	if run.ExecutionSessionID == nil {
		return nil
	}
	transcript, err := r.db.GetSession(r.ctx, *run.ExecutionSessionID)
	if err != nil {
		return fmt.Errorf("promptrun: load transcript session of run %s: %w", r.runID, err)
	}
	if err := registerTranscript(r.ctx, r.db, transcript); err != nil {
		return err
	}
	if len(notices) == 0 {
		return nil
	}
	if err := r.db.PutSessionNotices(r.ctx, transcript.ID, notices); err != nil {
		return fmt.Errorf("promptrun: record notices of run %s: %w", r.runID, err)
	}
	return nil
}
