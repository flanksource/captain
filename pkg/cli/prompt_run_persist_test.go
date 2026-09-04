package cli

import (
	"errors"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/ai/agent"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/promptrun"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistPromptRunRecordsNativeRun(t *testing.T) {
	db := withTestCaptainDB(t)
	rendered := PromptRenderResult{Name: "fix-bug", Model: "claude-sonnet-5", Provider: "anthropic", Mode: "agent"}
	rendered.Input.Prompt.User = "fix the failing test"
	batchID := uuid.New()

	persistPromptRun(t.Context(), promptRunRecordInput{
		Rendered: rendered, RunID: "run-1", SessionID: "0195c1de-4ab8-7000-8000-00000000abcd",
		Model: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent, BatchID: &batchID, ResultText: "done",
		ResultJSON: map[string]any{"answer": "42"},
	})

	session, err := db.GetSessionByIdentity(t.Context(), "0195c1de-4ab8-7000-8000-00000000abcd", "claude", "", "")
	require.NoError(t, err)
	runs, err := db.ListPromptRuns(t.Context(), database.PromptRunFilter{SessionID: &session.ID})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, database.PromptRunStateSucceeded, runs[0].State)
	assert.Equal(t, database.PromptRunPhaseFinished, runs[0].Phase)
	assert.Equal(t, "done", runs[0].ResultText)
	assert.Equal(t, map[string]any{"answer": "42"}, runs[0].ResultJSON)
	assert.Equal(t, "captain", runs[0].Origin)
	assert.Equal(t, "run-1", runs[0].AdmissionKey)
	assert.Equal(t, "fix the failing test", runs[0].PromptMarkdown)
	assert.NotEmpty(t, runs[0].RenderedSpec)
	require.NotNil(t, runs[0].BatchID)
	assert.Equal(t, batchID, *runs[0].BatchID)
	assert.Equal(t, "run", runs[0].Runtime.Mode)
	// Provider is the family that owns the model; Mode is the mechanism serving
	// it. One family runs on several modes, so the two must stay separate.
	assert.Equal(t, "anthropic", runs[0].Runtime.Resolved.Provider)
	assert.Equal(t, "agent", runs[0].Runtime.Resolved.Mode)
	assert.Equal(t, "claude-sonnet-5", runs[0].Runtime.Resolved.Model)

	t.Run("replay with the same run id is idempotent", func(t *testing.T) {
		persistPromptRun(t.Context(), promptRunRecordInput{
			Rendered: rendered, RunID: "run-1", SessionID: "0195c1de-4ab8-7000-8000-00000000abcd",
			Model: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent, ResultText: "done",
		})
		runs, err := db.ListPromptRuns(t.Context(), database.PromptRunFilter{SessionID: &session.ID})
		require.NoError(t, err)
		assert.Len(t, runs, 1)
	})

	t.Run("failed run records error and failed state", func(t *testing.T) {
		persistPromptRun(t.Context(), promptRunRecordInput{
			Rendered: rendered, RunID: "run-2", SessionID: "0195c1de-4ab8-7000-8000-00000000abcd",
			Model: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent, Error: "verify failed: tests red",
		})
		runs, err := db.ListPromptRuns(t.Context(), database.PromptRunFilter{SessionID: &session.ID})
		require.NoError(t, err)
		require.Len(t, runs, 2)
		failed := runs[0]
		if failed.AdmissionKey != "run-2" {
			failed = runs[1]
		}
		assert.Equal(t, database.PromptRunStateFailed, failed.State)
		assert.Equal(t, "verify failed: tests red", failed.Error)
	})
}

// A run that had to try twice is only legible if both attempts survive: the
// failing turn with the feedback that drove the retry, and the passing one that
// ended it. Without the rows a reader sees a green run and no account of the red
// one it started as.
func TestPersistPromptRunRecordsEveryIteration(t *testing.T) {
	db := withTestCaptainDB(t)
	const providerSession = "0195c1de-4ab8-7000-8000-0000000abcde"
	rendered := PromptRenderResult{Name: "fix-bug", Model: "claude-sonnet-5", Provider: "anthropic", Mode: "agent"}
	rendered.Input.Prompt.User = "fix the failing test"

	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	loop := loopWith(2, base, nil)
	verdicts := []agent.VerifyResult{
		{Valid: false, Iteration: 1, Report: verdictReport(1, false)},
		{Valid: true, Iteration: 2, Report: verdictReport(2, true)},
	}
	verdicts[0].Report.Feedback = "TestFoo failed"
	final, err := promptrun.FinalReport(verdicts)
	require.NoError(t, err)

	persistPromptRun(t.Context(), promptRunRecordInput{
		Rendered: rendered, RunID: "run-iter", SessionID: providerSession,
		Model: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent,
		ResultText: "fixed",
		ResultJSON: resultJSONWithVerify(map[string]any{"answer": "42"}, final),
		Iterations: promptrun.IterationRecords(promptrun.Result{Loop: loop, Verdicts: verdicts}, false),
	})

	session, err := db.GetSessionByIdentity(t.Context(), providerSession, "claude", "", "")
	require.NoError(t, err)
	runs, err := db.ListPromptRuns(t.Context(), database.PromptRunFilter{SessionID: &session.ID})
	require.NoError(t, err)
	require.Len(t, runs, 1)

	iterations, err := db.ListPromptRunIterations(t.Context(), runs[0].ID)
	require.NoError(t, err)
	require.Len(t, iterations, 2)

	first := iterations[0]
	assert.Equal(t, 1, first.Iteration)
	assert.Equal(t, database.PromptRunIterationStateFailed, first.State)
	assert.Equal(t, "TestFoo failed", first.Feedback)
	require.NotNil(t, first.VerificationResult)
	assert.False(t, first.VerificationResult.Passed)
	assert.Equal(t, 1, first.VerificationResult.Iteration)
	assert.Equal(t, map[string]any{"prompt": "attempt A"}, first.Request)
	require.NotNil(t, first.StartedAt)
	require.NotNil(t, first.FinishedAt)
	// The turn's own clock, not the clock at write time: the trigger's back-fill
	// would stamp both attempts with the moment the run ended.
	assert.Equal(t, base.UTC(), first.StartedAt.UTC())
	assert.Equal(t, base.Add(30*time.Second).UTC(), first.FinishedAt.UTC())

	second := iterations[1]
	assert.Equal(t, 2, second.Iteration)
	assert.Equal(t, database.PromptRunIterationStateSucceeded, second.State)
	require.NotNil(t, second.VerificationResult)
	assert.True(t, second.VerificationResult.Passed)

	// The final report also lands on the run itself, beside the prompt's answer.
	require.NotNil(t, runs[0].ResultJSON)
	assert.Equal(t, "42", runs[0].ResultJSON["answer"])
	verify, ok := runs[0].ResultJSON["verify"].(map[string]any)
	require.True(t, ok, "result_json.verify = %#v", runs[0].ResultJSON["verify"])
	assert.Equal(t, true, verify["passed"])

	report, iteration, err := db.LatestPromptRunVerification(t.Context(), runs[0].ID)
	require.NoError(t, err)
	assert.Equal(t, 2, iteration, "the run's verdict is the last turn's, not the first's")
	require.NotNil(t, report)
	assert.True(t, report.Passed)
}

// A run the stop button ended on turn 2 of 3 is still a run: the turn that
// completed, the verdict that judged it, and a row that says it was cancelled
// rather than that it failed. Returning before persistence left a stopped run
// with no row at all — no iterations, no result_json.verify, nothing to read.
func TestPersistPromptRunRecordsACancelledRun(t *testing.T) {
	db := withTestCaptainDB(t)
	const providerSession = "0195c1de-4ab8-7000-8000-0000000abce0"
	rendered := PromptRenderResult{Name: "fix-bug", Model: "claude-sonnet-5", Provider: "anthropic", Mode: "agent"}
	rendered.Input.Prompt.User = "fix the failing test"

	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	verdicts := []agent.VerifyResult{{Valid: false, Iteration: 1, Report: verdictReport(1, false)}}

	persistPromptRun(t.Context(), promptRunRecordInput{
		Rendered: rendered, RunID: "run-stopped", SessionID: providerSession,
		Model: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent,
		Error: "stopped", State: database.PromptRunStateCancelled,
		Iterations: promptrun.IterationRecords(promptrun.Result{Loop: loopWith(1, base, nil), Verdicts: verdicts}, true),
	})

	session, err := db.GetSessionByIdentity(t.Context(), providerSession, "claude", "", "")
	require.NoError(t, err)
	runs, err := db.ListPromptRuns(t.Context(), database.PromptRunFilter{SessionID: &session.ID})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, database.PromptRunStateCancelled, runs[0].State,
		"an interrupted run is neither succeeded nor failed")
	assert.Equal(t, "stopped", runs[0].Error)

	iterations, err := db.ListPromptRunIterations(t.Context(), runs[0].ID)
	require.NoError(t, err)
	require.Len(t, iterations, 1, "the turn that ran before the stop is the record of how far it got")
	assert.Equal(t, database.PromptRunIterationStateCancelled, iterations[0].State)
}

// failedRunRecord is the stamp the run-path applies when promptrun.Run returns
// an error, and it is the only thing that decides cancelled from failed.
func TestFailedRunRecord_CancelledVersusFailed(t *testing.T) {
	base := promptRunRecordInput{RunID: "r"}

	stopped := failedRunRecord(base, errors.New("stopped"), true)
	assert.Equal(t, database.PromptRunStateCancelled, stopped.State)
	assert.Equal(t, "stopped", stopped.Error)

	broke := failedRunRecord(base, errors.New("upstream 529"), false)
	assert.Equal(t, database.PromptRunStateFailed, broke.State)
	assert.Equal(t, "upstream 529", broke.Error)
}

// A turn whose report the store refuses must not take the run down with it: the
// run row is the record that the run happened at all, and the other turns are
// still true. The bad row is the only thing that goes missing, and loudly.
func TestPersistPromptRunKeepsTheRunWhenAnIterationIsRejected(t *testing.T) {
	db := withTestCaptainDB(t)
	const providerSession = "0195c1de-4ab8-7000-8000-0000000abcdf"
	rendered := PromptRenderResult{Name: "fix-bug", Model: "claude-sonnet-5", Provider: "anthropic", Mode: "agent"}
	rendered.Input.Prompt.User = "fix the failing test"

	base := time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC)
	corrupt := verdictReport(2, true)
	corrupt.State = api.VerifyStateFailed // passed=true with a failed state: Validate rejects it
	verdicts := []agent.VerifyResult{
		{Valid: false, Iteration: 1, Report: verdictReport(1, false)},
		{Valid: true, Iteration: 2, Report: corrupt},
	}

	persistPromptRun(t.Context(), promptRunRecordInput{
		Rendered: rendered, RunID: "run-partial", SessionID: providerSession,
		Model: "claude-sonnet-5", Provider: api.Anthropic, Mode: api.ModeAgent,
		ResultText: "fixed",
		Iterations: promptrun.IterationRecords(promptrun.Result{Loop: loopWith(2, base, nil), Verdicts: verdicts}, false),
	})

	session, err := db.GetSessionByIdentity(t.Context(), providerSession, "claude", "", "")
	require.NoError(t, err)
	runs, err := db.ListPromptRuns(t.Context(), database.PromptRunFilter{SessionID: &session.ID})
	require.NoError(t, err)
	require.Len(t, runs, 1, "the run row survives a rejected iteration")
	assert.Equal(t, database.PromptRunStateSucceeded, runs[0].State)
	assert.Equal(t, "fixed", runs[0].ResultText)

	iterations, err := db.ListPromptRunIterations(t.Context(), runs[0].ID)
	require.NoError(t, err)
	require.Len(t, iterations, 1, "the good turn is kept; only the rejected one is missing")
	assert.Equal(t, 1, iterations[0].Iteration)
}
