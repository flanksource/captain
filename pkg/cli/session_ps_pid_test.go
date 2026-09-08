package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/cmux"
)

// TestParsePSPIDsRejectsUnusableArgs verifies that a non-numeric or non-positive
// positional arg fails loudly rather than degrading into a full scan.
func TestParsePSPIDsRejectsUnusableArgs(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "non numeric", args: []string{"abc"}, want: `invalid PID "abc"`},
		{name: "zero", args: []string{"0"}, want: "PID must be positive, got 0"},
		{name: "negative", args: []string{"-5"}, want: "PID must be positive, got -5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parsePSPIDs(tc.args); err == nil || err.Error() != tc.want {
				t.Fatalf("parsePSPIDs(%q) error = %v, want %q", tc.args, err, tc.want)
			}
		})
	}

	pids, err := parsePSPIDs([]string{"10", " 20 ", ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(pids) != 2 || pids[0] != 10 || pids[1] != 20 {
		t.Fatalf("pids = %v, want [10 20]", pids)
	}
}

// TestRunPSInspectsNonAgentPID verifies the inspection contract: a named PID
// that is not an agent at all is still reported, with its runtime info and a
// synthetic "pid:<n>" identity rather than being filtered out.
func TestRunPSInspectsNonAgentPID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "shell")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	started := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	restore := stubPSInspection(t, func(pids []int) ([]agentProcess, error) {
		return []agentProcess{{
			Source: "zsh", PID: 4021, PPID: 4000, Status: "sleeping", Active: false,
			CWD: project, StartedAt: &started, Command: "-zsh", RSSBytes: 8 << 20,
			Environment: map[string]string{"SHELL": "/bin/zsh"},
		}}, nil
	})
	defer restore()

	result, err := RunPS(context.Background(), PSOptions{PIDs: []string{"4021"}, Source: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(result.Sessions))
	}
	rec := result.Sessions[0]
	if rec.ID != "pid:4021" {
		t.Fatalf("id = %q, want pid:4021", rec.ID)
	}
	if rec.Source != "zsh" {
		t.Fatalf("source = %q, want zsh", rec.Source)
	}
	if rec.Live == nil || rec.Live.PPID != 4000 || rec.Live.RSSBytes != 8<<20 {
		t.Fatalf("live runtime = %+v, want ppid 4000 and rss 8MiB", rec.Live)
	}
	if rec.Live.Environment["SHELL"] != "/bin/zsh" {
		t.Fatalf("environment = %v, want SHELL=/bin/zsh", rec.Live.Environment)
	}
	if result.Scope != "pid" {
		t.Fatalf("scope = %q, want pid", result.Scope)
	}
}

// TestRunPSPIDsBypassSourceAndProjectFilters verifies that naming a PID outranks
// the scan's filters — the user asked for that process, so neither its source
// nor its project may exclude it.
func TestRunPSPIDsBypassSourceAndProjectFilters(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "here")
	elsewhere := filepath.Join(home, "work", "elsewhere")
	for _, dir := range []string{project, elsewhere} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(project)

	restore := stubPSInspection(t, func(pids []int) ([]agentProcess, error) {
		return []agentProcess{{
			Source: "codex", PID: 777, Status: "active", Active: true,
			CWD: elsewhere, Command: "codex",
		}}, nil
	})
	defer restore()

	// --source claude and a project scope both exclude this codex process in a
	// normal scan; naming its PID must still report it.
	result, err := RunPS(context.Background(), PSOptions{
		PIDs: []string{"777"}, Source: "claude", Project: project,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 || result.Sessions[0].Live.PID != 777 {
		t.Fatalf("sessions = %+v, want the named pid 777", result.Sessions)
	}
}

// TestRunPSPropagatesDeadPIDError verifies a PID with no live process fails the
// command rather than returning an empty listing.
func TestRunPSPropagatesDeadPIDError(t *testing.T) {
	restore := stubPSInspection(t, func(pids []int) ([]agentProcess, error) {
		return nil, errors.New("no such process: 99999")
	})
	defer restore()

	_, err := RunPS(context.Background(), PSOptions{PIDs: []string{"99999"}, Source: "all"})
	if err == nil || err.Error() != "no such process: 99999" {
		t.Fatalf("err = %v, want no such process: 99999", err)
	}
}

// TestRunPSResolvesSessionFromEnvironment verifies the environment is a
// first-class identity source: a claude process whose session id appears only in
// CLAUDE_CODE_SESSION_ID resolves to that session and its transcript.
func TestRunPSResolvesSessionFromEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	transcript := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "env-sid.jsonl")
	writeJSONL(t, transcript, map[string]any{
		"type": "user", "sessionId": "env-sid", "timestamp": "2026-06-01T10:00:00Z", "cwd": project,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
	})

	restore := stubPSInspection(t, func(pids []int) ([]agentProcess, error) {
		return []agentProcess{{
			Source: "claude", PID: 5150, Status: "active", Active: true,
			CWD: project, Command: "claude",
			Environment: map[string]string{"CLAUDE_CODE_SESSION_ID": "env-sid"},
		}}, nil
	})
	defer restore()

	result, err := RunPS(context.Background(), PSOptions{PIDs: []string{"5150"}, Source: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(result.Sessions))
	}
	live := result.Sessions[0].Live
	if live == nil || live.SessionID != "env-sid" {
		t.Fatalf("session = %+v, want env-sid resolved from the environment", live)
	}
	if live.SessionFile != transcript {
		t.Fatalf("session file = %q, want %q", live.SessionFile, transcript)
	}
}

// TestRunPSEnvironmentSessionIDBeatsCwdNewest verifies that a declared id wins
// over the newest transcript in the project: a session too new to have written a
// transcript must not be mis-attributed to an older, unrelated one.
func TestRunPSEnvironmentSessionIDBeatsCwdNewest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	// An older, unrelated session is the only transcript on disk.
	stale := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "stale-sid.jsonl")
	writeJSONL(t, stale, map[string]any{
		"type": "user", "sessionId": "stale-sid", "timestamp": "2026-05-01T10:00:00Z", "cwd": project,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "old"}}},
	})

	restore := stubPSInspection(t, func(pids []int) ([]agentProcess, error) {
		return []agentProcess{{
			Source: "claude", PID: 6060, Status: "active", Active: true,
			CWD: project, Command: "claude",
			Environment: map[string]string{"CLAUDE_CODE_SESSION_ID": "fresh-sid"},
		}}, nil
	})
	defer restore()

	result, err := RunPS(context.Background(), PSOptions{PIDs: []string{"6060"}, Source: "all"})
	if err != nil {
		t.Fatal(err)
	}
	live := result.Sessions[0].Live
	if live == nil || live.SessionID != "fresh-sid" {
		t.Fatalf("session = %+v, want fresh-sid (declared id must beat cwd-newest)", live)
	}
}

// TestRunPSResolvesCodexThreadFromEnvironment verifies a codex process with no
// open rollout still reports the thread id its launcher stamped on it.
func TestRunPSResolvesCodexThreadFromEnvironment(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	restore := stubPSInspection(t, func(pids []int) ([]agentProcess, error) {
		return []agentProcess{{
			Source: "codex", PID: 8080, Status: "active", Active: true,
			CWD: project, Command: "codex",
			Environment: map[string]string{"CODEX_THREAD_ID": "thread-42"},
		}}, nil
	})
	defer restore()

	result, err := RunPS(context.Background(), PSOptions{PIDs: []string{"8080"}, Source: "all"})
	if err != nil {
		t.Fatal(err)
	}
	live := result.Sessions[0].Live
	if live == nil || live.SessionID != "thread-42" {
		t.Fatalf("session = %+v, want thread-42 resolved from the environment", live)
	}
}

// stubPSInspection swaps the PID-inspection seam (and neutralises the live lsof
// and cmux lookups) so RunPS's PID mode can be driven without real processes.
func stubPSInspection(t *testing.T, inspect func([]int) ([]agentProcess, error)) func() {
	t.Helper()
	origInspect := inspectSessionProcesses
	origFiles := discoverOpenSessionFiles
	origCmux := discoverCmuxSurfaces
	inspectSessionProcesses = func(_ context.Context, pids []int) ([]agentProcess, error) {
		return inspect(pids)
	}
	discoverOpenSessionFiles = func(pids []int) map[int][]string { return nil }
	discoverCmuxSurfaces = func() (map[string]cmux.Surface, error) { return nil, nil }
	return func() {
		inspectSessionProcesses = origInspect
		discoverOpenSessionFiles = origFiles
		discoverCmuxSurfaces = origCmux
	}
}
