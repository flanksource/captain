package database

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListPromptRunsFiltersAndOrdersDeterministically(t *testing.T) {
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres store tests")
	}

	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_prompt_run_list",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	db, err := Open(t.Context(), Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{Source: "codex", Provider: "openai"})
	require.NoError(t, err)
	otherSession, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{Source: "claude", Provider: "anthropic"})
	require.NoError(t, err)

	firstID := uuid.MustParse("10000000-0000-0000-0000-000000000001")
	first, err := db.CreatePromptRun(t.Context(), CreatePromptRunInput{ID: firstID, SessionID: session.ID})
	require.NoError(t, err)
	finished := PromptRunPhaseFinished
	succeeded := PromptRunStateSucceeded
	_, err = db.UpdatePromptRun(t.Context(), UpdatePromptRunInput{
		ID: first.ID, ExpectedVersion: first.Version, Phase: &finished, State: &succeeded,
	})
	require.NoError(t, err)

	secondID := uuid.MustParse("10000000-0000-0000-0000-000000000002")
	_, err = db.CreatePromptRun(t.Context(), CreatePromptRunInput{ID: secondID, SessionID: session.ID})
	require.NoError(t, err)
	_, err = db.CreatePromptRun(t.Context(), CreatePromptRunInput{
		ID: uuid.MustParse("10000000-0000-0000-0000-000000000003"), SessionID: otherSession.ID,
	})
	require.NoError(t, err)

	sharedQueuedAt := time.Date(2026, time.July, 12, 10, 0, 0, 0, time.UTC)
	require.NoError(t, db.Gorm().Exec(
		`UPDATE captain_prompt_runs SET queued_at = ? WHERE id IN (?, ?)`, sharedQueuedAt, firstID, secondID,
	).Error)

	listed, err := db.ListPromptRuns(t.Context(), PromptRunFilter{SessionID: &session.ID})
	require.NoError(t, err)
	require.Len(t, listed, 2)
	assert.Equal(t, secondID, listed[0].ID)
	assert.Equal(t, firstID, listed[1].ID)

	pending := PromptRunStatePending
	listed, err = db.ListPromptRuns(t.Context(), PromptRunFilter{SessionID: &session.ID, State: &pending})
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, secondID, listed[0].ID)

	unknown := PromptRunState("unknown")
	_, err = db.ListPromptRuns(t.Context(), PromptRunFilter{State: &unknown})
	assert.ErrorIs(t, err, ErrInvalidPromptRun)

	emptySessionID := uuid.Nil
	_, err = db.ListPromptRuns(t.Context(), PromptRunFilter{SessionID: &emptySessionID})
	assert.ErrorIs(t, err, ErrInvalidPromptRun)
}
