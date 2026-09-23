package cli

import (
	"encoding/json"
	"fmt"
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
	if err := pinGoToolchainEnv(); err != nil {
		panic("pkg/cli tests: " + err.Error())
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

// pinGoToolchainEnv resolves the Go toolchain's cache locations to absolute
// paths and exports them, so the HOME override above cannot relocate them.
//
// The HOME this package hands itself is for captain's own config. But the Go
// toolchain derives GOPATH, GOCACHE and GOMODCACHE from HOME whenever they are
// unset, so every `go build` a test shells out to — captainBinary builds
// ./cmd/captain — inherited an empty module cache and an empty build cache and
// re-downloaded and recompiled the entire dependency tree, cgo sqlite3
// included. On CI that was ten minutes for this one package, with the runner's
// warm caches sitting untouched. It must run before HOME is replaced.
func pinGoToolchainEnv() error {
	names := []string{"GOCACHE", "GOMODCACHE", "GOPATH"}
	out, err := exec.Command("go", append([]string{"env", "-json"}, names...)...).Output()
	if err != nil {
		return fmt.Errorf("resolve the Go toolchain environment: %w", err)
	}
	var resolved map[string]string
	if err := json.Unmarshal(out, &resolved); err != nil {
		return fmt.Errorf("decode `go env -json %v`: %w", names, err)
	}
	for _, name := range names {
		if resolved[name] == "" {
			return fmt.Errorf("`go env -json` reported no %s; a toolchain subprocess would derive it from the throwaway HOME", name)
		}
		if err := os.Setenv(name, resolved[name]); err != nil {
			return fmt.Errorf("pin %s: %w", name, err)
		}
	}
	return nil
}

// A toolchain subprocess started from this package must reach the same caches
// as the rest of the build. Left to inherit the throwaway HOME it reaches none
// of them, which is invisible locally and cost ten minutes per CI run.
func TestGoToolchainCachesSurviveTheHomeOverride(t *testing.T) {
	home := os.Getenv("HOME")
	if home == "" {
		t.Fatal("TestMain is expected to hand this package its own HOME")
	}
	for _, name := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		value := os.Getenv(name)
		if value == "" {
			t.Errorf("%s is unset, so `go build` derives it from HOME and gets a cold cache", name)
			continue
		}
		if rel, err := filepath.Rel(home, value); err == nil && !strings.HasPrefix(rel, "..") {
			t.Errorf("%s = %q sits inside the throwaway HOME %q, so every toolchain subprocess gets a cold cache", name, value, home)
		}
	}
}
