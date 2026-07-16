package database

import (
	"os"
	"path/filepath"
	"testing"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/stretchr/testify/require"
)

func TestCaptainMigrationsAreIdempotentAndShareOnePool(t *testing.T) {
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres migration tests")
	}

	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_contract",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	shared, err := commonsdb.NewGorm(dsn, commonsdb.DefaultGormConfig())
	require.NoError(t, err)
	sharedSQL, err := shared.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sharedSQL.Close()) })

	first, err := Open(t.Context(), Config{Gorm: shared, DSN: dsn})
	require.NoError(t, err)
	require.Same(t, shared, first.Gorm())
	require.NoError(t, first.Close(), "closing an injected handle must not close the shared pool")
	require.NoError(t, sharedSQL.PingContext(t.Context()))

	scriptRuns := func() map[string]string {
		var rows []struct {
			Path      string
			UpdatedAt string
		}
		require.NoError(t, shared.Raw(`SELECT path, updated_at::text AS updated_at
			FROM schema_migration_scripts WHERE scope = 'captain'`).Scan(&rows).Error)
		runs := map[string]string{}
		for _, row := range rows {
			runs[row.Path] = row.UpdatedAt
		}
		return runs
	}
	firstRuns := scriptRuns()
	require.NotEmpty(t, firstRuns)

	second, err := Open(t.Context(), Config{Gorm: shared, DSN: dsn})
	require.NoError(t, err, "applying the Captain bundle twice must be idempotent")
	require.Same(t, shared, second.Gorm())
	require.Equal(t, firstRuns, scriptRuns(),
		"a no-op apply must not re-run any hash-gated script (steady state performs zero DDL)")

	for _, table := range []string{
		"captain_sessions",
		"captain_prompt_runs",
		"captain_prompt_run_iterations",
		"captain_plans",
		"captain_plan_revisions",
		"captain_turns",
		"captain_model_calls",
		"captain_events",
		"captain_turn_requests",
	} {
		require.True(t, shared.Migrator().HasTable(table), "%s should exist", table)
	}

	for table, columns := range map[string][]string{
		"captain_sessions":              {"id", "lifecycle_status", "activity_state", "health_state", "state_version"},
		"captain_prompt_runs":           {"id", "session_id", "root_session_id", "phase", "state", "version"},
		"captain_prompt_run_iterations": {"id", "prompt_run_id", "state"},
		"captain_plans":                 {"id", "source_session_id", "approved_revision_id", "approval_state"},
		"captain_plan_revisions":        {"id", "plan_id", "revision"},
		"captain_turns":                 {"id", "session_id", "status"},
		"captain_model_calls":           {"id", "turn_id", "prompt_run_id", "iteration_id", "status"},
		"captain_events":                {"id", "session_id", "turn_id", "prompt_run_id", "iteration_id", "kind"},
		"captain_turn_requests":         {"id", "session_id", "turn_id", "prompt_run_id", "plan_id", "state", "version"},
	} {
		for _, column := range columns {
			require.True(t, shared.Migrator().HasColumn(table, column), "%s.%s should exist", table, column)
		}
	}

	for _, view := range []string{
		"captain_session_overview",
		"captain_session_turns",
		"captain_session_plans",
		"captain_session_costs",
		"captain_session_events",
		"captain_prompt_run_overview",
	} {
		var exists bool
		require.NoError(t, shared.Raw(`SELECT EXISTS (
			SELECT 1 FROM pg_catalog.pg_views WHERE schemaname = 'public' AND viewname = ?
		)`, view).Scan(&exists).Error)
		require.True(t, exists, "%s should exist", view)
	}
}
