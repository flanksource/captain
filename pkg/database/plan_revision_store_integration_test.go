package database

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendPlanRevisionWithResultIsAuthoritativeUnderConcurrency(t *testing.T) {
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres store tests")
	}

	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_plan_revision_result",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	db, err := Open(t.Context(), WithDSN(dsn), WithMigrations())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	session, err := db.CreateOrGetSession(t.Context(), CreateSessionInput{Source: "test", Provider: "test"})
	require.NoError(t, err)
	plan, err := db.CreateOrGetPlan(t.Context(), CreatePlanInput{
		ID: uuid.New(), SourceSessionID: session.ID, Variant: "concurrent",
	})
	require.NoError(t, err)

	type result struct {
		revision *PlanRevision
		created  bool
		err      error
	}
	const callers = 8
	results := make(chan result, callers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			revision, created, err := db.AppendPlanRevisionWithResult(t.Context(), AppendPlanRevisionInput{
				PlanID: plan.ID, PlanMarkdown: "# Shared plan\n\n- preserve data\n", CreatedBy: "test",
			})
			results <- result{revision: revision, created: created, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	createdCount := 0
	var revisionID uuid.UUID
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.revision)
		if revisionID == uuid.Nil {
			revisionID = result.revision.ID
		}
		assert.Equal(t, revisionID, result.revision.ID)
		assert.Equal(t, 1, result.revision.Revision)
		if result.created {
			createdCount++
		}
	}
	assert.Equal(t, 1, createdCount, "exactly one locked caller must report creating the revision")

	replayed, created, err := db.AppendPlanRevisionWithResult(t.Context(), AppendPlanRevisionInput{
		PlanID: plan.ID, PlanMarkdown: "\r\n# Shared plan\r\n\r\n- preserve data\r\n",
	})
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, revisionID, replayed.ID)

	compatible, err := db.AppendPlanRevision(t.Context(), AppendPlanRevisionInput{
		PlanID: plan.ID, PlanMarkdown: "# Shared plan\n\n- preserve data",
	})
	require.NoError(t, err)
	assert.Equal(t, revisionID, compatible.ID)

	revisions, err := db.ListPlanRevisions(t.Context(), plan.ID)
	require.NoError(t, err)
	require.Len(t, revisions, 1)
	assert.Equal(t, revisionID, revisions[0].ID)

	linkedPlan, err := db.CreateOrGetPlan(t.Context(), CreatePlanInput{
		ID: uuid.New(), SourceSessionID: session.ID, Variant: "foreign-key-linked",
	})
	require.NoError(t, err)
	keyShare := db.Gorm().Begin()
	require.NoError(t, keyShare.Error)
	defer keyShare.Rollback()
	var lockedID string
	require.NoError(t, keyShare.Raw(
		`SELECT id::text FROM captain_plans WHERE id = ? FOR KEY SHARE`, linkedPlan.ID,
	).Scan(&lockedID).Error)
	assert.Equal(t, linkedPlan.ID.String(), lockedID)

	type appendResult struct {
		revision *PlanRevision
		created  bool
		err      error
	}
	appended := make(chan appendResult, 1)
	go func() {
		revision, created, err := db.AppendPlanRevisionWithResult(t.Context(), AppendPlanRevisionInput{
			PlanID: linkedPlan.ID, PlanMarkdown: "# Referenced plan",
		})
		appended <- appendResult{revision: revision, created: created, err: err}
	}()
	select {
	case result := <-appended:
		require.NoError(t, result.err)
		require.NotNil(t, result.revision)
		assert.True(t, result.created)
	case <-time.After(2 * time.Second):
		require.NoError(t, keyShare.Rollback().Error)
		result := <-appended
		require.NoError(t, result.err)
		t.Fatal("plan revision append blocked behind a compatible foreign-key KEY SHARE lock")
	}
	require.NoError(t, keyShare.Commit().Error)
}
