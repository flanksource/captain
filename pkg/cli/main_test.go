package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/flanksource/captain/pkg/captainconfig"
)

// TestMain disables the embedded-postgres session store by default so unit tests
// degrade to uncached summarization instead of booting a postgres server (slow,
// and not permitted in the sandbox). Integration tests that want a real store
// set CAPTAIN_SESSION_DB_URL to a DSN before running.
//
// It also gives the package its own HOME. Config loading now rejects removed
// keys loudly, so a developer's real ~/.captain.yaml could fail specs that never
// meant to read it — including the ones that shell out to a captain subprocess,
// which a path override inside this process would not reach. Individual tests
// still redirect captainconfig themselves when they need seeded settings; their
// cleanups reset the override to "use HOME", which now lands here.
func TestMain(m *testing.M) {
	if _, ok := os.LookupEnv("CAPTAIN_SESSION_DB_URL"); !ok {
		_ = os.Setenv("CAPTAIN_SESSION_DB_URL", "off")
	}
	home, err := os.MkdirTemp("", "captain-cli-home")
	if err != nil {
		panic("pkg/cli tests: isolate HOME: " + err.Error())
	}
	_ = os.Setenv("HOME", home)
	captainconfig.SetPathForTesting(filepath.Join(home, ".captain.yaml"))
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}
