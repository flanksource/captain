package database

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPromptRunIterationRejectsInvalidInputBeforeWriting(t *testing.T) {
	db := openPromptRunIterationDB(t, "captain_prompt_run_iteration_validation")
	run := newPromptRunForIterations(t, db)

	inconsistent := verifyReportFixture("inconsistent", 7)
	inconsistent.Passed = true // the tree still carries a failure, so State stays "failed"
	rejected := []struct {
		name  string
		input UpsertPromptRunIterationInput
	}{
		{"negative iteration", UpsertPromptRunIterationInput{
			PromptRunID: run.ID, Iteration: -1, State: PromptRunIterationStateRunning}},
		{"zero iteration", UpsertPromptRunIterationInput{
			PromptRunID: run.ID, Iteration: 0, State: PromptRunIterationStateRunning}},
		{"unknown state", UpsertPromptRunIterationInput{
			PromptRunID: run.ID, Iteration: 7, State: PromptRunIterationState("verifying")}},
		{"empty prompt run", UpsertPromptRunIterationInput{
			Iteration: 7, State: PromptRunIterationStateRunning}},
		{"self-inconsistent verification report", UpsertPromptRunIterationInput{
			PromptRunID: run.ID, Iteration: 7, State: PromptRunIterationStateSucceeded,
			VerificationResult: &inconsistent}},
		{"report stamped for another iteration", UpsertPromptRunIterationInput{
			PromptRunID: run.ID, Iteration: 2, State: PromptRunIterationStateSucceeded,
			VerificationResult: ptr(verifyReportFixture("iteration 3 verification", 3))}},
		{"finished before started", UpsertPromptRunIterationInput{
			PromptRunID: run.ID, Iteration: 7, State: PromptRunIterationStateSucceeded,
			StartedAt:  ptr(time.Date(2026, time.September, 3, 9, 0, 0, 0, time.UTC)),
			FinishedAt: ptr(time.Date(2026, time.September, 3, 8, 0, 0, 0, time.UTC))}},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.UpsertPromptRunIteration(t.Context(), tc.input)
			assert.ErrorIs(t, err, ErrInvalidPromptRun)
		})
	}

	iterations, err := db.ListPromptRunIterations(t.Context(), run.ID)
	require.NoError(t, err)
	assert.Empty(t, iterations, "a rejected upsert must not have written a row")

	_, err = db.ListPromptRunIterations(t.Context(), uuid.Nil)
	assert.ErrorIs(t, err, ErrInvalidPromptRun)
	_, _, err = db.LatestPromptRunVerification(t.Context(), uuid.Nil)
	assert.ErrorIs(t, err, ErrInvalidPromptRun)
}

// TestPromptRunVerificationReaderRejectsCorruptStoredReports covers rows the store
// itself would never write: a JSONB `null` (which passes `IS NOT NULL`) must not
// decode into a blank passing report, and a report that fails Validate must surface
// as an error rather than render as an empty verdict.
func TestPromptRunVerificationReaderRejectsCorruptStoredReports(t *testing.T) {
	db := openPromptRunIterationDB(t, "captain_prompt_run_iteration_corrupt")

	jsonNullRun := newPromptRunForIterations(t, db)
	insertRawIterationVerification(t, db, jsonNullRun.ID, 1, `null`)
	report, verified, err := db.LatestPromptRunVerification(t.Context(), jsonNullRun.ID)
	require.NoError(t, err)
	assert.Nil(t, report, "a JSONB null is the absence of a report, not a blank passing one")
	assert.Zero(t, verified)

	validRun := newPromptRunForIterations(t, db)
	valid := verifyReportFixture("iteration 1 verification", 1)
	_, err = db.UpsertPromptRunIteration(t.Context(), UpsertPromptRunIterationInput{
		PromptRunID: validRun.ID, Iteration: 1, State: PromptRunIterationStateFailed, VerificationResult: &valid,
	})
	require.NoError(t, err)
	insertRawIterationVerification(t, db, validRun.ID, 2, `null`)
	report, verified, err = db.LatestPromptRunVerification(t.Context(), validRun.ID)
	require.NoError(t, err)
	require.NotNil(t, report)
	assert.Equal(t, 1, verified, "a JSONB-null row is skipped, so the last real report wins")
	assert.Equal(t, valid, *report)

	corruptRun := newPromptRunForIterations(t, db)
	insertRawIterationVerification(t, db, corruptRun.ID, 1,
		`{"kind":"cmd","name":"corrupt","ran":true,"passed":true,"state":"failed","summary":{}}`)
	_, _, err = db.LatestPromptRunVerification(t.Context(), corruptRun.ID)
	require.Error(t, err, "a stored report that fails Validate must surface, not render as a blank pass")
	assert.Contains(t, err.Error(), "passed=true with state")
}

func insertRawIterationVerification(t *testing.T, db *DB, runID uuid.UUID, iteration int, verification string) {
	t.Helper()
	require.NoError(t, db.Gorm().Exec(
		`INSERT INTO captain_prompt_run_iterations (prompt_run_id, iteration, state, verification_result)
		 VALUES (?, ?, 'failed', ?::jsonb)`, runID, iteration, verification).Error)
}
