package cli

import (
	"os"
	"testing"
)

// TestMain disables the embedded-postgres session store by default so unit tests
// degrade to uncached summarization instead of booting a postgres server (slow,
// and not permitted in the sandbox). Integration tests that want a real store
// set CAPTAIN_SESSION_DB_URL to a DSN before running.
func TestMain(m *testing.M) {
	if _, ok := os.LookupEnv("CAPTAIN_SESSION_DB_URL"); !ok {
		_ = os.Setenv("CAPTAIN_SESSION_DB_URL", "off")
	}
	os.Exit(m.Run())
}
