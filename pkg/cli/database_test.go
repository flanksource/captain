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
