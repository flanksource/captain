package container

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteBaseContext(t *testing.T) {
	dir := t.TempDir()
	if err := writeBaseContext(dir); err != nil {
		t.Fatalf("writeBaseContext: %v", err)
	}

	for _, name := range []string{"Dockerfile", "entrypoint.sh"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s not written: %v", name, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", name)
		}
	}
}

func TestEnsureBaseImageSkipsCustomBase(t *testing.T) {
	// A non-default base image should return nil without any docker calls.
	if err := EnsureBaseImage("custom:img"); err != nil {
		t.Errorf("expected nil for custom base, got: %v", err)
	}
}
