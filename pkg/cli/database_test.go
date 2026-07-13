package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/monitor"
	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/stretchr/testify/require"
)

func TestCaptainDSNPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	t.Run("gavel primary env wins", func(t *testing.T) {
		t.Setenv(gavelDBEnvDSN, "postgres://primary/gavel")
		t.Setenv(gavelCacheEnvDSN, "postgres://cache/gavel")
		t.Setenv(captainSessionEnvDSN, "postgres://captain/db")
		dsn, source, err := captainDSN()
		require.NoError(t, err)
		require.Equal(t, "postgres://primary/gavel", dsn)
		require.Equal(t, gavelDBEnvDSN, source)
	})

	t.Run("cache env is next", func(t *testing.T) {
		t.Setenv(gavelDBEnvDSN, "")
		t.Setenv(gavelCacheEnvDSN, "postgres://cache/gavel")
		t.Setenv(captainSessionEnvDSN, "postgres://captain/db")
		dsn, source, err := captainDSN()
		require.NoError(t, err)
		require.Equal(t, "postgres://cache/gavel", dsn)
		require.Equal(t, gavelCacheEnvDSN, source)
	})

	t.Run("captain env is next", func(t *testing.T) {
		t.Setenv(gavelDBEnvDSN, "")
		t.Setenv(gavelCacheEnvDSN, "")
		t.Setenv(captainSessionEnvDSN, "postgres://captain/db")
		dsn, source, err := captainDSN()
		require.NoError(t, err)
		require.Equal(t, "postgres://captain/db", dsn)
		require.Equal(t, captainSessionEnvDSN, source)
	})

	t.Run("gavel db.json mode=dsn is used without env", func(t *testing.T) {
		t.Setenv(gavelDBEnvDSN, "")
		t.Setenv(gavelCacheEnvDSN, "")
		t.Setenv(captainSessionEnvDSN, "")
		dir := filepath.Join(home, ".config", "gavel")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "db.json"),
			[]byte(`{"mode":"dsn","dsn":"postgres://from/config"}`), 0o644))
		dsn, source, err := captainDSN()
		require.NoError(t, err)
		require.Equal(t, "postgres://from/config", dsn)
		require.Contains(t, source, "db.json")
	})

	t.Run("invalid db.json mode fails loudly", func(t *testing.T) {
		t.Setenv(gavelDBEnvDSN, "")
		t.Setenv(gavelCacheEnvDSN, "")
		t.Setenv(captainSessionEnvDSN, "")
		dir := filepath.Join(home, ".config", "gavel")
		require.NoError(t, os.MkdirAll(dir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "db.json"),
			[]byte(`{"mode":"bogus"}`), 0o644))
		_, _, err := captainDSN()
		require.Error(t, err)
	})
}

// withTestCaptainDB starts an isolated embedded postgres, injects it as the
// process-wide captain database, and fakes live-process discovery so tests
// never touch a configured DSN or the host's real processes.
func withTestCaptainDB(t *testing.T, processes ...monitor.Process) *database.DB {
	t.Helper()
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres cli tests")
	}
	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_cli",
	})
	require.NoError(t, err)
	db, err := database.Open(t.Context(), database.Config{DSN: dsn})
	require.NoError(t, err)

	setCaptainDBForTest(db)
	monitorDiscoverProcesses = func() ([]monitor.Process, error) { return processes, nil }
	t.Cleanup(func() {
		setCaptainDBForTest(nil)
		monitorDiscoverProcesses = nil
		require.NoError(t, db.Close())
		require.NoError(t, stop())
	})
	return db
}
