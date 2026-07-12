package cli

import (
	"os"
	"path/filepath"
	"testing"

	captaindb "github.com/flanksource/captain/pkg/database"
	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveNativePlanUsesPersistedApprovedContentWithoutSourceFile(t *testing.T) {
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres plan lookup tests")
	}

	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_native_plan_lookup",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })
	db, err := captaindb.Open(t.Context(), captaindb.Config{DSN: dsn})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })

	session, err := db.CreateOrGetSession(t.Context(), captaindb.CreateSessionInput{
		ProviderSessionID: "provider-plan-session", Source: "codex", Provider: "openai",
	})
	require.NoError(t, err)
	deletedPath := filepath.Join(t.TempDir(), "deleted-plan.md")
	require.NoError(t, os.WriteFile(deletedPath, []byte("stale disk content"), 0o600))
	plan, err := db.CreateOrGetPlan(t.Context(), captaindb.CreatePlanInput{
		SourceSessionID: session.ID, Variant: "approved", Path: deletedPath, Slug: "durable",
	})
	require.NoError(t, err)
	first, err := db.AppendPlanRevision(t.Context(), captaindb.AppendPlanRevisionInput{
		PlanID: plan.ID, PlanMarkdown: "# Approved durable plan",
	})
	require.NoError(t, err)
	_, err = db.AppendPlanRevision(t.Context(), captaindb.AppendPlanRevisionInput{
		PlanID: plan.ID, PlanMarkdown: "# Newer but unapproved plan",
	})
	require.NoError(t, err)
	_, err = db.ApprovePlanRevision(t.Context(), captaindb.ApprovePlanRevisionInput{
		PlanID: plan.ID, RevisionID: first.ID,
	})
	require.NoError(t, err)
	require.NoError(t, os.Remove(deletedPath))

	result, ok, err := resolveNativePlan(t.Context(), db.Gorm(), "provider-plan-session", "all")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, session.ID.String(), result.SessionID)
	assert.Equal(t, plan.ID.String(), result.PlanID)
	assert.Equal(t, first.ID.String(), result.RevisionID)
	assert.Equal(t, "# Approved durable plan", result.Content)
	assert.False(t, result.OnDisk)

	byUUID, ok, err := resolveNativePlan(t.Context(), db.Gorm(), session.ID.String(), "codex")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, result.Content, byUUID.Content)
}
