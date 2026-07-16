package cli

import (
	"testing"

	"github.com/flanksource/captain/pkg/database"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPersistPromptRunRecordsNativeRun(t *testing.T) {
	db := withTestCaptainDB(t)
	rendered := PromptRenderResult{Name: "fix-bug", Model: "claude-sonnet-5", Backend: "claude-agent"}
	rendered.Input.Prompt.User = "fix the failing test"
	batchID := uuid.New()

	persistPromptRun(t.Context(), promptRunRecordInput{
		Rendered: rendered, RunID: "run-1", SessionID: "0195c1de-4ab8-7000-8000-00000000abcd",
		Model: "claude-sonnet-5", Backend: "claude-agent", BatchID: &batchID, ResultText: "done",
	})

	session, err := db.GetSessionByIdentity(t.Context(), "0195c1de-4ab8-7000-8000-00000000abcd", "claude", "", "")
	require.NoError(t, err)
	runs, err := db.ListPromptRuns(t.Context(), database.PromptRunFilter{SessionID: &session.ID})
	require.NoError(t, err)
	require.Len(t, runs, 1)
	assert.Equal(t, database.PromptRunStateSucceeded, runs[0].State)
	assert.Equal(t, database.PromptRunPhaseFinished, runs[0].Phase)
	assert.Equal(t, "done", runs[0].ResultText)
	assert.Equal(t, "captain", runs[0].Origin)
	assert.Equal(t, "run-1", runs[0].AdmissionKey)
	assert.Equal(t, "fix the failing test", runs[0].PromptMarkdown)
	assert.NotEmpty(t, runs[0].RenderedSpec)
	require.NotNil(t, runs[0].BatchID)
	assert.Equal(t, batchID, *runs[0].BatchID)
	assert.Equal(t, "run", runs[0].Runtime.Mode)
	assert.Equal(t, "claude-agent", runs[0].Runtime.Resolved.Provider)
	assert.Equal(t, "claude-agent", runs[0].Runtime.Resolved.Backend)
	assert.Equal(t, "claude-sonnet-5", runs[0].Runtime.Resolved.Model)

	t.Run("replay with the same run id is idempotent", func(t *testing.T) {
		persistPromptRun(t.Context(), promptRunRecordInput{
			Rendered: rendered, RunID: "run-1", SessionID: "0195c1de-4ab8-7000-8000-00000000abcd",
			Model: "claude-sonnet-5", Backend: "claude-agent", ResultText: "done",
		})
		runs, err := db.ListPromptRuns(t.Context(), database.PromptRunFilter{SessionID: &session.ID})
		require.NoError(t, err)
		assert.Len(t, runs, 1)
	})

	t.Run("failed run records error and failed state", func(t *testing.T) {
		persistPromptRun(t.Context(), promptRunRecordInput{
			Rendered: rendered, RunID: "run-2", SessionID: "0195c1de-4ab8-7000-8000-00000000abcd",
			Model: "claude-sonnet-5", Backend: "claude-agent", Error: "verify failed: tests red",
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
