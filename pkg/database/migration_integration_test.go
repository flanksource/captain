package database

import (
	"testing"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/require"
)

func TestCaptainMigrationsAreIdempotentAndShareOnePool(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_contract"})
	dsn, shared := handle.DSN(), handle.Gorm()
	sharedSQL, err := shared.DB()
	require.NoError(t, err)

	first, err := Open(t.Context(), WithGorm(shared), WithDSN(dsn), WithMigrations())
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

	second, err := Open(t.Context(), WithGorm(shared), WithDSN(dsn), WithMigrations())
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
	require.False(t, shared.Migrator().HasTable("captain_outbox"),
		"the retired outbox must not remain after migration")

	var retiredObjectCount int64
	require.NoError(t, shared.Raw(`SELECT count(*)
		FROM pg_catalog.pg_proc
		WHERE pronamespace = 'public'::regnamespace
		  AND proname IN ('captain_emit_session_change', 'captain_notify_outbox')`).Scan(&retiredObjectCount).Error)
	require.Zero(t, retiredObjectCount, "retired outbox functions must be removed")
	require.NoError(t, shared.Raw(`SELECT count(*)
		FROM pg_catalog.pg_trigger
		WHERE NOT tgisinternal
		  AND (tgname LIKE 'captain_%_emit_after' OR tgname = 'captain_outbox_notify_after')`).Scan(&retiredObjectCount).Error)
	require.Zero(t, retiredObjectCount, "retired outbox triggers must be removed")

	var activityTriggerCount int64
	require.NoError(t, shared.Raw(`SELECT count(*)
		FROM pg_catalog.pg_trigger
		WHERE NOT tgisinternal AND tgname = 'captain_messages_activity_after'`).Scan(&activityTriggerCount).Error)
	require.EqualValues(t, 1, activityTriggerCount, "session activity projection must remain installed")

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
