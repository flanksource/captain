package migrations

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/flanksource/commons-db/dbtest"
	"github.com/stretchr/testify/require"
)

func TestConcurrentApplySerializesCaptainMigrations(t *testing.T) {
	handle := dbtest.ForT(t, dbtest.Options{Name: "captain_concurrent_migrations"})
	dsn := handle.DSN()

	// Hold the same session lock before releasing a group of Apply calls. This
	// proves every caller enters through the advisory-lock boundary rather than
	// racing the Atlas inspect/diff/apply window.
	blocker, err := acquireMigrationLock(t.Context(), applyRequest{Connection: dsn, Schema: DefaultSchema})
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

	db := handle.SQL()
	var sessionsTable *string
	require.NoError(t, db.QueryRowContext(t.Context(),
		`SELECT to_regclass('public.captain_sessions')::text`).Scan(&sessionsTable))
	require.NotNil(t, sessionsTable)
	require.Equal(t, "captain_sessions", *sessionsTable)

	// All Apply calls returned, so their session locks must have been released.
	// Bound the reacquisition to catch a leaked dedicated connection cleanly.
	reacquireCtx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	reacquired, err := acquireMigrationLock(reacquireCtx, applyRequest{Connection: dsn, Schema: DefaultSchema})
	require.NoError(t, err)
	require.NoError(t, reacquired.Close())
}
