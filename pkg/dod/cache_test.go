package dod

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestWriteReadDelete(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	sessionID := "test-session-123"

	dod := &DodFile{
		Commands:  []string{"make test", "make lint"},
		Workdir:   "/tmp/project",
		Timeout:   300,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
	}

	if err := Write(sessionID, dod); err != nil {
		t.Fatalf("Write: %v", err)
	}

	if _, err := os.Stat(filepath.Join(tmpDir, ".claude", "dod", sessionID+".json")); err != nil {
		t.Fatalf("cache file not created: %v", err)
	}

	got, err := Read(sessionID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(got.Commands) != 2 || got.Commands[0] != "make test" || got.Commands[1] != "make lint" {
		t.Errorf("commands mismatch: %v", got.Commands)
	}
	if got.Workdir != "/tmp/project" {
		t.Errorf("workdir = %q, want /tmp/project", got.Workdir)
	}
	if got.Timeout != 300 {
		t.Errorf("timeout = %d, want 300", got.Timeout)
	}
	if !got.CreatedAt.Equal(dod.CreatedAt) {
		t.Errorf("created_at = %v, want %v", got.CreatedAt, dod.CreatedAt)
	}

	if !Exists(sessionID) {
		t.Error("Exists returned false for existing file")
	}

	if err := Delete(sessionID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if Exists(sessionID) {
		t.Error("Exists returned true after deletion")
	}
}

func TestReadNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	_, err := Read("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent session")
	}
}

func TestWriteOverwrites(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	sessionID := "overwrite-test"

	first := &DodFile{Commands: []string{"make test"}, Workdir: "/a", Timeout: 60, CreatedAt: time.Now().UTC()}
	if err := Write(sessionID, first); err != nil {
		t.Fatalf("Write first: %v", err)
	}

	second := &DodFile{Commands: []string{"make lint"}, Workdir: "/b", Timeout: 120, CreatedAt: time.Now().UTC()}
	if err := Write(sessionID, second); err != nil {
		t.Fatalf("Write second: %v", err)
	}

	got, err := Read(sessionID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got.Commands) != 1 || got.Commands[0] != "make lint" {
		t.Errorf("expected overwritten commands, got %v", got.Commands)
	}
	if got.Workdir != "/b" {
		t.Errorf("workdir = %q, want /b", got.Workdir)
	}
}

func TestDeleteNonExistent(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	if err := Delete("does-not-exist"); err != nil {
		t.Errorf("Delete nonexistent should not error: %v", err)
	}
}

func TestWriteWithLastRun(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)

	sessionID := "lastrun-test"
	now := time.Now().UTC().Truncate(time.Second)

	dod := &DodFile{
		Commands:  []string{"make test"},
		Workdir:   "/tmp",
		Timeout:   300,
		CreatedAt: now,
		LastRun: &LastRun{
			At: now,
			Results: []CommandResult{
				{Command: "make test", ExitCode: 1, Passed: false, Stderr: "FAIL"},
			},
		},
	}

	if err := Write(sessionID, dod); err != nil {
		t.Fatalf("Write: %v", err)
	}

	got, err := Read(sessionID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.LastRun == nil {
		t.Fatal("LastRun is nil")
	}
	if len(got.LastRun.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(got.LastRun.Results))
	}
	if got.LastRun.Results[0].ExitCode != 1 || got.LastRun.Results[0].Passed {
		t.Errorf("unexpected result: %+v", got.LastRun.Results[0])
	}
}
