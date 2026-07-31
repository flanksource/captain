package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/monitor"
	"github.com/flanksource/commons-db/dbtest"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestConfigureNativeDatabase(t *testing.T) {
	setCaptainDBForTest(nil)
	t.Cleanup(func() { setCaptainDBForTest(nil) })

	first := &gorm.DB{}
	require.NoError(t, ConfigureNativeDatabase(first))
	require.NoError(t, ConfigureNativeDatabase(first), "reconfiguring the same pool should be idempotent")

	db, err := captainDB(t.Context())
	require.NoError(t, err)
	require.Same(t, first, db.Gorm())

	err = ConfigureNativeDatabase(&gorm.DB{})
	require.EqualError(t, err, "native Captain database is already configured with a different pool")
}

func TestConfigureNativeDatabaseRejectsNil(t *testing.T) {
	setCaptainDBForTest(nil)
	t.Cleanup(func() { setCaptainDBForTest(nil) })

	err := ConfigureNativeDatabase(nil)
	require.EqualError(t, err, "captain database GORM pool is nil")
}

func TestCaptainDSNPrecedence(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	databaseURL = ""
	t.Cleanup(func() { databaseURL = "" })

	t.Run("db-url flag wins", func(t *testing.T) {
		flags := pflag.NewFlagSet("captain", pflag.ContinueOnError)
		BindDatabaseURLFlag(flags)
		require.NoError(t, flags.Parse([]string{"--db-url", "postgres://flag/captain"}))
		t.Cleanup(func() { databaseURL = "" })

		t.Setenv(gavelDBEnvDSN, "postgres://primary/gavel")
		t.Setenv(gavelCacheEnvDSN, "postgres://cache/gavel")
		t.Setenv(captainSessionEnvDSN, "postgres://captain/db")
		dsn, source, err := captainDSN()
		require.NoError(t, err)
		require.Equal(t, "postgres://flag/captain", dsn)
		require.Equal(t, "--db-url", source)
	})

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

// withTestCaptainDB leases an isolated database, injects it as the process-wide
// captain database, and fakes live-process discovery so tests never touch a
// configured DSN or the host's real processes.
func withTestCaptainDB(t *testing.T, processes ...monitor.Process) *database.DB {
	t.Helper()
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_cli"})
	db, err := database.Open(t.Context(), database.WithDSN(handle.DSN()), database.WithMigrations())
	require.NoError(t, err)

	setCaptainDBForTest(db)
	monitorDiscoverProcesses = func() ([]monitor.Process, error) { return processes, nil }
	t.Cleanup(func() {
		setCaptainDBForTest(nil)
		monitorDiscoverProcesses = nil
		require.NoError(t, db.Close())
	})
	return db
}
