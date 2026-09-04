package promptrun

import (
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/commons/logger"
)

// IterationRecords turns one finished run into the rows
// captain_prompt_run_iterations holds: what each turn was asked, how it ended,
// and the verdict that judged it. It is the one place a host — captain's own
// CLI or an embedding one — derives those rows from, so every host files the
// same account of a run.
//
// It reads the runner's own records — ai.LoopResult.Iterations for the turns
// that executed and agent.VerifyResult for the verdicts — rather than keeping a
// parallel tally that could disagree with them. The two are joined on the turn
// number, which both sides already carry: the loop indexes from 0 and a verdict
// names its turn from 1, the same numbering the store is keyed on.
//
// A turn is judged by every verifier the workflow declares, so a round holds one
// verdict per verifier. All of them are rolled into the turn's single stored
// report (FinalReport → api.MergeReports); keeping only the last one filed a
// `commands` + `fixture` round as the fixture alone.
//
// A verify-only run has no loop: nothing was generated, the workflow's
// verifiers judged the tree as it stood. That is still one iteration — the
// first — and its verdict is the run's whole account, so it is filed as
// iteration 1. A run with neither turns nor verdicts has nothing to file.
//
// stopped says the run was interrupted — its context ended the loop, whether
// because the user pressed stop or because the run's deadline fired. Its last
// turn was cut off rather than judged, and calling that "failed" would blame the
// work for the interruption.
func IterationRecords(result Result, stopped bool) []database.UpsertPromptRunIterationInput {
	if result.Loop == nil {
		return verifyOnlyRecords(result.Verdicts, stopped)
	}
	byIteration := make(map[int][]agent.VerifyResult, len(result.Verdicts))
	for _, verdict := range result.Verdicts {
		byIteration[verdict.Iteration] = append(byIteration[verdict.Iteration], verdict)
	}

	records := make([]database.UpsertPromptRunIterationInput, 0, len(result.Loop.Iterations))
	for i, turn := range result.Loop.Iterations {
		iteration := turn.Iteration + 1
		record := database.UpsertPromptRunIterationInput{
			Iteration: iteration,
			Request:   map[string]any{"prompt": turn.Request.Prompt.User},
			State:     database.PromptRunIterationStateSucceeded,
		}
		if !turn.StartedAt.IsZero() {
			started := turn.StartedAt
			record.StartedAt = &started
		}
		if !turn.FinishedAt.IsZero() {
			// Both timestamps or neither: the store's state trigger back-fills a
			// missing finished_at from its own clock, which is long after the turn.
			finished := turn.FinishedAt
			record.FinishedAt = &finished
		}
		if round := byIteration[iteration]; len(round) > 0 {
			judge(&record, round)
		}
		if turn.Err != nil {
			// A turn the provider could not complete was never judged; the error is
			// the whole account of it.
			record.State = database.PromptRunIterationStateFailed
			record.Error = turn.Err.Error()
		}
		if stopped && i == len(result.Loop.Iterations)-1 {
			record.State = database.PromptRunIterationStateCancelled
		}
		records = append(records, record)
	}
	return records
}

// verifyOnlyRecords is the single row of a run that generated nothing: the
// verifiers' round, filed as iteration 1, bracketed by the report's own clock
// since there was no provider call to time.
func verifyOnlyRecords(verdicts []agent.VerifyResult, stopped bool) []database.UpsertPromptRunIterationInput {
	if len(verdicts) == 0 {
		return nil
	}
	record := database.UpsertPromptRunIterationInput{
		Iteration: 1,
		Request:   map[string]any{"verify_only": true},
		State:     database.PromptRunIterationStateSucceeded,
	}
	judge(&record, verdicts)
	if report := record.VerificationResult; report != nil {
		record.StartedAt, record.FinishedAt = report.StartedAt, report.FinishedAt
	}
	if stopped {
		record.State = database.PromptRunIterationStateCancelled
	}
	return []database.UpsertPromptRunIterationInput{record}
}

// judge stamps a round's rolled-up verdict onto the turn's record. A round that
// cannot be rolled up still happened and the row must say so; what goes
// missing is the verdict, and loudly, in both the log and the row.
func judge(record *database.UpsertPromptRunIterationInput, round []agent.VerifyResult) {
	report, err := FinalReport(round)
	if err != nil {
		logger.Errorf("prompt run iteration %d: rolling up its %d verdict(s) failed: %v", record.Iteration, len(round), err)
		record.Error = err.Error()
	}
	record.VerificationResult = report
	if report != nil {
		record.Feedback = report.Feedback
	}
	if !Passed(round) {
		record.State = database.PromptRunIterationStateFailed
	}
}
