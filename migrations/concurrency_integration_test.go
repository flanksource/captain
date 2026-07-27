package migrations

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/stretchr/testify/require"
)

func TestConcurrentApplySerializesCaptainMigrations(t *testing.T) {
	if os.Getenv("CAPTAIN_DB_EMBEDDED_TEST") == "" {
		t.Skip("set CAPTAIN_DB_EMBEDDED_TEST=1 to run embedded-postgres migration tests")
	}

	dsn, stop, err := commonsdb.StartEmbedded(commonsdb.EmbeddedConfig{
		DataDir:  filepath.Join(t.TempDir(), "postgres"),
		Database: "captain_concurrent_migrations",
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, stop()) })

	// Hold the same session lock before releasing a group of Apply calls. This
	// proves every caller enters through the advisory-lock boundary rather than
	// racing the Atlas inspect/diff/apply window.
	blocker, err := acquireMigrationLock(t.Context(), dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, blocker.Close()) })

	const callers = 6
	results := make(chan error, callers)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(callers)
	for range callers {
		go func() {
			ready.Done()
			<-start
			results <- Apply(t.Context(), dsn)
		}()
	}
	ready.Wait()
	close(start)

	select {
	case err := <-results:
		t.Fatalf("Apply completed while the Captain migration lock was held: %v", err)
	case <-time.After(250 * time.Millisecond):
	}

	require.NoError(t, blocker.Close())
	for range callers {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(90 * time.Second):
			t.Fatal("timed out waiting for serialized Captain migrations")
		}
	}

	db, err := commonsdb.NewDB(dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	var sessionsTable *string
	require.NoError(t, db.QueryRowContext(t.Context(),
		`SELECT to_regclass('public.captain_sessions')::text`).Scan(&sessionsTable))
	require.NotNil(t, sessionsTable)
	require.Equal(t, "captain_sessions", *sessionsTable)

	// All Apply calls returned, so their session locks must have been released.
	// Bound the reacquisition to catch a leaked dedicated connection cleanly.
	reacquireCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	reacquired, err := acquireMigrationLock(reacquireCtx, dsn)
	require.NoError(t, err)
	require.NoError(t, reacquired.Close())
}
