package cli

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
)

// promptRunIterationRecords turns one finished run into the rows
// captain_prompt_run_iterations holds: what each turn was asked, how it ended,
// and the verdict that judged it.
//
// It reads the runner's own records — ai.LoopResult.Iterations for the turns
// that executed and agent.VerifyResult for the verdicts — rather than keeping a
// parallel tally that could disagree with them. The two are joined on the turn
// number, which both sides already carry: the loop indexes from 0 and a verdict
// names its turn from 1, the same numbering the store is keyed on.
//
// A turn is judged by every verifier the workflow declares, so a round holds one
// verdict per verifier. All of them are rolled into the turn's single stored
// report (promptrun.FinalReport → api.MergeReports); keeping only the last one
// filed a `commands` + `fixture` round as the fixture alone.
//
// stopped says the run was interrupted — its context ended the loop, whether
// because the user pressed stop or because the run's deadline fired. Its last
// turn was cut off rather than judged, and calling that "failed" would blame the
// work for the interruption.
func promptRunIterationRecords(loop *ai.LoopResult, verdicts []agent.VerifyResult, stopped bool) []database.UpsertPromptRunIterationInput {
	if loop == nil {
		return nil
	}
	byIteration := make(map[int][]agent.VerifyResult, len(verdicts))
	for _, verdict := range verdicts {
		byIteration[verdict.Iteration] = append(byIteration[verdict.Iteration], verdict)
	}

	records := make([]database.UpsertPromptRunIterationInput, 0, len(loop.Iterations))
	for i, turn := range loop.Iterations {
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
			report, err := promptrun.FinalReport(round)
			if err != nil {
				// The turn happened and the row must still say so; the verdict is
				// what goes missing, and loudly, in both the log and the row.
				log.Errorf("prompt run iteration %d: rolling up its %d verdict(s) failed: %v", iteration, len(round), err)
				record.Error = err.Error()
			}
			record.VerificationResult = report
			if report != nil {
				record.Feedback = report.Feedback
			}
			if !promptrun.Passed(round) {
				record.State = database.PromptRunIterationStateFailed
			}
		}
		if turn.Err != nil {
			// A turn the provider could not complete was never judged; the error is
			// the whole account of it.
			record.State = database.PromptRunIterationStateFailed
			record.Error = turn.Err.Error()
		}
		if stopped && i == len(loop.Iterations)-1 {
			record.State = database.PromptRunIterationStateCancelled
		}
		records = append(records, record)
	}
	return records
}

// resultJSONWithVerify puts the run's final report on result_json under
// `verify`, beside the prompt's own structured output. It copies rather than
// mutating: the same map is the CLI summary's StructuredOutput, which is the
// prompt's answer and nothing else.
func resultJSONWithVerify(structured map[string]any, report *api.VerifyReport) map[string]any {
	if report == nil {
		return structured
	}
	raw, err := json.Marshal(report)
	if err != nil {
		log.Errorf("prompt run result_json: encoding the verify report of %q failed: %v", report.Name, err)
		return structured
	}
	var encoded map[string]any
	if err := json.Unmarshal(raw, &encoded); err != nil {
		log.Errorf("prompt run result_json: decoding the verify report of %q failed: %v", report.Name, err)
		return structured
	}
	merged := make(map[string]any, len(structured)+1)
	for key, value := range structured {
		merged[key] = value
	}
	merged["verify"] = encoded
	return merged
}
