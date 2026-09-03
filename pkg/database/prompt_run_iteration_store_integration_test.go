package database

import (
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptRunIterationUpsertAndLatestVerification(t *testing.T) {
	db := openPromptRunIterationDB(t, "captain_prompt_run_iterations")
	run := newPromptRunForIterations(t, db)

	report, verified, err := db.LatestPromptRunVerification(t.Context(), run.ID)
	require.NoError(t, err)
	assert.Nil(t, report, "a run with no iteration has no verification")
	assert.Zero(t, verified)

	startedAt := time.Date(2026, time.September, 3, 9, 0, 0, 0, time.UTC)
	first, err := db.UpsertPromptRunIteration(t.Context(), UpsertPromptRunIterationInput{
		PromptRunID: run.ID, Iteration: 1, State: PromptRunIterationStateRunning,
		Request: map[string]any{"prompt": "implement the store"}, StartedAt: &startedAt,
	})
	require.NoError(t, err)
	assert.Equal(t, PromptRunIterationStateRunning, first.State)

	finishedAt := startedAt.Add(90 * time.Second)
	replayed, err := db.UpsertPromptRunIteration(t.Context(), UpsertPromptRunIterationInput{
		PromptRunID: run.ID, Iteration: 1, State: PromptRunIterationStateFailed,
		Feedback: "tests failed; retry with the failing case", Error: "exit status 1", FinishedAt: &finishedAt,
	})
	require.NoError(t, err)
	assert.Equal(t, first.ID, replayed.ID, "(prompt_run_id, iteration) identifies the row; a replay must update it")
	assert.Equal(t, PromptRunIterationStateFailed, replayed.State)
	assert.Equal(t, "tests failed; retry with the failing case", replayed.Feedback)
	assert.Equal(t, "exit status 1", replayed.Error)
	assert.Equal(t, map[string]any{"prompt": "implement the store"}, replayed.Request,
		"a replay that omits the request must not erase it")
	require.NotNil(t, replayed.StartedAt)
	assert.Equal(t, startedAt, replayed.StartedAt.UTC(), "a replay that omits started_at must not erase it")
	require.NotNil(t, replayed.FinishedAt)
	assert.Equal(t, finishedAt, replayed.FinishedAt.UTC())

	_, err = db.UpsertPromptRunIteration(t.Context(), UpsertPromptRunIterationInput{
		PromptRunID: run.ID, Iteration: 2, State: PromptRunIterationStateFailed,
		VerificationResult: ptr(verifyReportFixture("iteration 2 verification", 2)),
	})
	require.NoError(t, err)

	latest := verifyReportFixture("iteration 3 verification", 3)
	_, err = db.UpsertPromptRunIteration(t.Context(), UpsertPromptRunIterationInput{
		PromptRunID: run.ID, Iteration: 3, State: PromptRunIterationStateSucceeded,
		VerificationResult: &latest,
	})
	require.NoError(t, err)
	_, err = db.UpsertPromptRunIteration(t.Context(), UpsertPromptRunIterationInput{
		PromptRunID: run.ID, Iteration: 4, State: PromptRunIterationStateRunning,
	})
	require.NoError(t, err)

	report, verified, err = db.LatestPromptRunVerification(t.Context(), run.ID)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 3, verified, "the newest iteration carrying a report wins, not the newest iteration")
	assert.Equal(t, latest, *report, "the whole report round-trips, nested children and checklist included")

	var stored string
	require.NoError(t, db.Gorm().Raw(
		`SELECT verification_result::text FROM captain_prompt_run_iterations WHERE prompt_run_id = ? AND iteration = ?`,
		run.ID, 3).Scan(&stored).Error)
	assert.Contains(t, stored, `"cel_expression"`, "the stored report keeps the snake_case wire shape")
	assert.Contains(t, stored, `"exit_code"`)

	iterations, err := db.ListPromptRunIterations(t.Context(), run.ID)
	require.NoError(t, err)
	require.Len(t, iterations, 4, "a replay must not insert a second row for the same iteration")
	for i, iteration := range iterations {
		assert.Equal(t, i+1, iteration.Iteration, "iterations list in ascending 1-based iteration order")
	}

	overview, err := db.GetPromptRunOverview(t.Context(), run.ID)
	require.NoError(t, err)
	require.NotNil(t, overview.LatestVerification)
	assert.Equal(t, latest, *overview.LatestVerification)
	assert.Equal(t, 4, overview.CurrentIteration,
		"captain_sync_prompt_run_iteration tracks the highest 1-based iteration written")
}

// TestPromptRunIterationTerminalReplayBackfillsFinishedAt pins the UPDATE half of
// captain_prompt_run_iterations_state_before: a replay that reports a terminal
// state without a finished_at still gets one, and does not lose started_at.
func TestPromptRunIterationTerminalReplayBackfillsFinishedAt(t *testing.T) {
	db := openPromptRunIterationDB(t, "captain_prompt_run_iteration_backfill")
	run := newPromptRunForIterations(t, db)

	// The trigger back-fills finished_at from clock_timestamp(), and
	// captain_prompt_run_iterations_time_order rejects finished_at < started_at, so
	// the iteration must have started in the past for the back-fill to be legal.
	startedAt := time.Now().UTC().Add(-90 * time.Second).Truncate(time.Microsecond)
	running, err := db.UpsertPromptRunIteration(t.Context(), UpsertPromptRunIterationInput{
		PromptRunID: run.ID, Iteration: 1, State: PromptRunIterationStateRunning, StartedAt: &startedAt,
	})
	require.NoError(t, err)
	require.Nil(t, running.FinishedAt, "a running iteration has not finished")

	finished, err := db.UpsertPromptRunIteration(t.Context(), UpsertPromptRunIterationInput{
		PromptRunID: run.ID, Iteration: 1, State: PromptRunIterationStateSucceeded,
	})
	require.NoError(t, err)
	require.NotNil(t, finished.FinishedAt, "the state trigger back-fills finished_at on a terminal UPDATE")
	assert.False(t, finished.FinishedAt.Before(startedAt))
	require.NotNil(t, finished.StartedAt)
	assert.Equal(t, startedAt, finished.StartedAt.UTC(), "back-filling finished_at must not move started_at")
}

// TestPromptRunIterationReplayClearsVerificationResult documents the last-write-wins
// contract: a replay is a full statement of the iteration, so dropping the report
// clears it and the latest-verification reader falls back to the earlier turn.
func TestPromptRunIterationReplayClearsVerificationResult(t *testing.T) {
	db := openPromptRunIterationDB(t, "captain_prompt_run_iteration_replay")
	run := newPromptRunForIterations(t, db)

	firstReport := verifyReportFixture("iteration 1 verification", 1)
	_, err := db.UpsertPromptRunIteration(t.Context(), UpsertPromptRunIterationInput{
		PromptRunID: run.ID, Iteration: 1, State: PromptRunIterationStateFailed,
		VerificationResult: &firstReport, Feedback: "retry", Error: "exit status 1",
	})
	require.NoError(t, err)
	_, err = db.UpsertPromptRunIteration(t.Context(), UpsertPromptRunIterationInput{
		PromptRunID: run.ID, Iteration: 2, State: PromptRunIterationStateFailed,
		VerificationResult: ptr(verifyReportFixture("iteration 2 verification", 2)),
	})
	require.NoError(t, err)

	replayed, err := db.UpsertPromptRunIteration(t.Context(), UpsertPromptRunIterationInput{
		PromptRunID: run.ID, Iteration: 2, State: PromptRunIterationStateRunning,
	})
	require.NoError(t, err)
	assert.Nil(t, replayed.VerificationResult, "a replay without a report clears the stored one")

	report, verified, err := db.LatestPromptRunVerification(t.Context(), run.ID)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 1, verified, "with iteration 2's report cleared the reader falls back to iteration 1")
	assert.Equal(t, firstReport, *report)
}

func openPromptRunIterationDB(t *testing.T, name string) *DB {
	t.Helper()
	handle := dbtest.ForT(t, dbtest.Options{Name: name})
	db, err := Open(t.Context(), WithDSN(handle.DSN()), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return db
}

func newPromptRunForIterations(t *testing.T, db *DB) *PromptRun {
	t.Helper()
	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{Source: "codex", Provider: "openai"})
	require.NoError(t, err)
	run, err := db.CreatePromptRun(t.Context(), CreatePromptRunInput{SessionID: session.ID})
	require.NoError(t, err)
	require.NotNil(t, run)
	return run
}

// verifyReportFixture is a failing two-leaf report: one group node with a passed
// and a failed child, plus a checklist item, so a round-trip exercises nesting,
// pointer fields, and the snake_case context keys.
func verifyReportFixture(name string, iteration int) api.VerifyReport {
	itemPassed := false
	report := api.VerifyReport{
		Kind: api.VerifyKindCmd, Name: name, Ran: true, Passed: false,
		Reason: "1 of 2 tests failed", Feedback: "fix the failing assertion", Iteration: iteration,
		State: api.VerifyStateFailed,
		Tests: []api.VerifyNode{{
			Name:      "pkg/database",
			Framework: "go",
			Children: []api.VerifyNode{
				{Name: "upsert is idempotent", Passed: true, Duration: 1500 * time.Millisecond},
				{Name: "latest verification wins", Failed: true, Message: "expected 2, got 3",
					Context: &api.VerifyNodeContext{
						Command: "go test ./pkg/database/...", ExitCode: 1,
						CELExpression: "verify.summary.failed == 0", Expected: float64(0), Actual: float64(1),
					}},
			},
		}},
		Checklist: []api.VerifyChecklistItem{
			{Item: "iterations persist their verification", Passed: &itemPassed, Message: "not yet"},
		},
	}
	report.Summary = api.SummarizeNodes(report.Tests)
	return report
}

func ptr[T any](value T) *T { return &value }
