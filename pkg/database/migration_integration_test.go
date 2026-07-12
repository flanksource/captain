package database

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/migrations"
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

	second, err := Open(t.Context(), Config{Gorm: shared, DSN: dsn})
	require.NoError(t, err, "applying the Captain bundle twice must be idempotent")
	require.Same(t, shared, second.Gorm())

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

func TestCaptainMigrationsRejectLegacySessionCacheWithoutMutation(t *testing.T) {
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres migration tests")
	}

	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_legacy_preflight",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	legacy, err := commonsdb.NewGorm(dsn, commonsdb.DefaultGormConfig())
	require.NoError(t, err)
	legacySQL, err := legacy.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, legacySQL.Close()) })

	require.NoError(t, legacy.Exec(`CREATE TABLE public.captain_sessions (
		path text PRIMARY KEY,
		id text,
		source text,
		mod_unix bigint,
		title text
	)`).Error)
	require.NoError(t, legacy.Exec(`INSERT INTO public.captain_sessions
		(path, id, source, mod_unix, title)
		VALUES ('/tmp/session.jsonl', 'legacy-session', 'codex', 42, 'preserve me')`).Error)

	_, err = Open(t.Context(), Config{DSN: dsn})
	require.ErrorIs(t, err, migrations.ErrLegacySessionSchema)
	require.ErrorContains(t, err, "explicit legacy session backfill/cutover")

	var preserved struct {
		Path    string
		ID      string
		Source  string
		ModUnix int64
		Title   string
	}
	require.NoError(t, legacy.Raw(`SELECT path, id, source, mod_unix, title
		FROM public.captain_sessions WHERE path = '/tmp/session.jsonl'`).Scan(&preserved).Error)
	require.Equal(t, "/tmp/session.jsonl", preserved.Path)
	require.Equal(t, "legacy-session", preserved.ID)
	require.Equal(t, "codex", preserved.Source)
	require.EqualValues(t, 42, preserved.ModUnix)
	require.Equal(t, "preserve me", preserved.Title)
	require.False(t, legacy.Migrator().HasColumn("captain_sessions", "lifecycle_status"))
	require.False(t, legacy.Migrator().HasTable("captain_prompt_runs"))

	var idType string
	require.NoError(t, legacy.Raw(`SELECT data_type
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'captain_sessions'
		  AND column_name = 'id'`).Scan(&idType).Error)
	require.Equal(t, "text", idType)

	var migrationMetadataExists bool
	require.NoError(t, legacy.Raw(`SELECT to_regclass('public.schema_migration_scripts') IS NOT NULL`).Scan(&migrationMetadataExists).Error)
	require.False(t, migrationMetadataExists, "preflight must fail before commons-db/migrate creates metadata")

}
