package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	keepGoCaches()
	home, err := os.MkdirTemp("", "captain-cli-home")
	if err != nil {
		panic("pkg/cli tests: isolate HOME: " + err.Error())
	}
	_ = os.Setenv("HOME", home)
	captainconfig.SetPathForTesting(filepath.Join(home, ".captain.yaml"))
	code := m.Run()
	_ = os.RemoveAll(home)
	if captainBinPath != "" {
		_ = os.RemoveAll(filepath.Dir(captainBinPath))
	}
	os.Exit(code)
}

// keepGoCaches pins Go's module and build caches where they are before HOME moves. Specs that
// run go (the e2e specs build captain) would otherwise fill the isolated HOME with a module cache
// of their own, gigabytes of read-only files that RemoveAll leaves behind.
func keepGoCaches() {
	out, err := exec.Command("go", "env", "GOMODCACHE", "GOCACHE").Output()
	if err != nil {
		// Without a go toolchain no spec runs go.
		return
	}
	caches := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(caches) == 2 {
		_ = os.Setenv("GOMODCACHE", caches[0])
		_ = os.Setenv("GOCACHE", caches[1])
	}
}
