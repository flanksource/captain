package database

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetPlanReviewStatePersistsNonApprovalDecisions(t *testing.T) {
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres store tests")
	}

	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_plan_review_state",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	db, err := Open(t.Context(), Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{Source: "test", Provider: "test"})
	require.NoError(t, err)
	plan, err := db.CreateOrGetPlan(t.Context(), CreatePlanInput{SourceSessionID: session.ID, Variant: "review"})
	require.NoError(t, err)
	revision, err := db.AppendPlanRevision(t.Context(), AppendPlanRevisionInput{PlanID: plan.ID, PlanMarkdown: "# Durable plan"})
	require.NoError(t, err)

	revisionRequested, err := db.SetPlanReviewState(t.Context(), SetPlanReviewStateInput{
		PlanID: plan.ID, State: PlanApprovalRevisionRequested, Actor: "reviewer", Comment: "add rollback coverage",
	})
	require.NoError(t, err)
	assert.Equal(t, PlanApprovalRevisionRequested, revisionRequested.ApprovalState)
	assert.Equal(t, "reviewer", revisionRequested.ApprovedBy)
	assert.Equal(t, "add rollback coverage", revisionRequested.ApprovalComment)
	assert.Nil(t, revisionRequested.ApprovedRevisionID)
	assert.NotNil(t, revisionRequested.FeedbackAt)

	replayed, err := db.SetPlanReviewState(t.Context(), SetPlanReviewStateInput{
		PlanID: plan.ID, State: PlanApprovalRevisionRequested, Actor: "reviewer", Comment: "add rollback coverage",
	})
	require.NoError(t, err)
	assert.Equal(t, revisionRequested.UpdatedAt, replayed.UpdatedAt)

	approved, err := db.ApprovePlanRevision(t.Context(), ApprovePlanRevisionInput{
		PlanID: plan.ID, RevisionID: revision.ID, ApprovedBy: "reviewer",
	})
	require.NoError(t, err)
	require.NotNil(t, approved.ApprovedRevisionID)

	rejected, err := db.SetPlanReviewState(t.Context(), SetPlanReviewStateInput{
		PlanID: plan.ID, State: PlanApprovalRejected, Actor: "reviewer", Comment: "wrong approach",
	})
	require.NoError(t, err)
	assert.Equal(t, PlanApprovalRejected, rejected.ApprovalState)
	assert.Nil(t, rejected.ApprovedRevisionID)
	assert.Nil(t, rejected.ApprovedRevision)
	require.NotNil(t, rejected.ApprovalCreatedAt)
	assert.Nil(t, rejected.FeedbackAt)

	replayedRejection, err := db.SetPlanReviewState(t.Context(), SetPlanReviewStateInput{
		PlanID: plan.ID, State: PlanApprovalRejected, Actor: "reviewer", Comment: "wrong approach",
	})
	require.NoError(t, err)
	assert.Equal(t, rejected.UpdatedAt, replayedRejection.UpdatedAt)
	assert.Equal(t, rejected.ApprovalCreatedAt, replayedRejection.ApprovalCreatedAt)

	updatedRejection, err := db.SetPlanReviewState(t.Context(), SetPlanReviewStateInput{
		PlanID: plan.ID, State: PlanApprovalRejected, Actor: "second-reviewer", Comment: "still the wrong approach",
	})
	require.NoError(t, err)
	require.NotNil(t, updatedRejection.ApprovalCreatedAt)
	assert.True(t, updatedRejection.ApprovalCreatedAt.After(*rejected.ApprovalCreatedAt))
	assert.Equal(t, "second-reviewer", updatedRejection.ApprovedBy)
	assert.Equal(t, "still the wrong approach", updatedRejection.ApprovalComment)

	_, err = db.SetPlanReviewState(t.Context(), SetPlanReviewStateInput{PlanID: plan.ID, State: PlanApprovalApproved})
	assert.ErrorIs(t, err, ErrInvalidPlan)
	_, err = db.SetPlanReviewState(t.Context(), SetPlanReviewStateInput{PlanID: uuid.New(), State: PlanApprovalRejected})
	assert.True(t, errors.Is(err, ErrPlanNotFound))
}
