// Covers how the e2e suite obtains the compiled binary. The build it would
// otherwise run happens inside a test, so it competes with that test's deadline
// — and a dependency bump, which empties the build cache, is exactly when it
// gets slow enough to matter.
package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaptainBinaryUsesAPrebuiltOne(t *testing.T) {
	prebuilt := filepath.Join(t.TempDir(), "captain")
	if err := os.WriteFile(prebuilt, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(captainBinaryEnv, prebuilt)

	if got := resolveCaptainBinary(t); got != prebuilt {
		t.Errorf("resolveCaptainBinary() = %q, want the prebuilt binary %q", got, prebuilt)
	}
}

// A path that does not exist is a mistake in how the suite was invoked, not a
// reason to silently spend minutes rebuilding.
func TestCaptainBinaryRejectsAMissingPrebuiltPath(t *testing.T) {
	t.Setenv(captainBinaryEnv, filepath.Join(t.TempDir(), "absent"))

	if _, err := prebuiltCaptainBinary(); err == nil {
		t.Error("a prebuilt path that does not exist must be reported, not ignored")
	}
}

// Unset means the suite builds for itself, so `go test ./...` still works with
// no setup.
func TestCaptainBinaryFallsBackToBuilding(t *testing.T) {
	t.Setenv(captainBinaryEnv, "")

	path, err := prebuiltCaptainBinary()
	if err != nil {
		t.Fatalf("an unset variable must not be an error: %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want empty so the caller builds", path)
	}
}
