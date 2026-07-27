package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/cmux"
)

// TestRunPSStartsFromProcessesAndAugments verifies the process-first flow: a
// live claude process is enriched with its CMUX surface + open transcript
// (session id, sub-agent id, last activity) and augmented from the on-disk
// session, producing a single live record.
func TestRunPSStartsFromProcessesAndAugments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	root := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "sess-ps.jsonl")
	writeJSONL(t, root,
		map[string]any{
			"type": "user", "sessionId": "sess-ps", "timestamp": "2026-06-01T10:00:00Z", "cwd": project,
			"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
		},
		map[string]any{
			"type": "assistant", "sessionId": "sess-ps", "timestamp": "2026-06-01T10:00:02Z", "cwd": project,
			"message": map[string]any{"role": "assistant", "model": "claude-sonnet-4",
				"content": []any{map[string]any{"type": "text", "text": "ok"}}},
		},
	)
	subagent := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project), "sess-ps", "subagents", "agent-abc123.jsonl")
	writeJSONL(t, subagent, map[string]any{
		"type": "user", "sessionId": "sess-ps", "timestamp": "2026-06-01T10:00:01Z", "cwd": project,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "sub"}}},
	})

	surface := &CmuxSurface{SurfaceID: "SURFACE-1", TabID: "TAB-1", AgentKind: "claude", Port: 9150}
	started := time.Date(2026, 6, 1, 9, 59, 0, 0, time.UTC)

	restore := stubPSDiscovery(t,
		func() ([]agentProcess, error) {
			return []agentProcess{{
				Source: "claude", PID: 4242, Status: "active", Active: true,
				CWD: project, StartedAt: &started,
				Command: "claude --session-id sess-ps",
			}}, nil
		},
		func(pids []int) map[int][]string { return map[int][]string{4242: {root, subagent}} },
		func(pid int) *CmuxSurface { return surface },
		map[string]cmux.Surface{"SURFACE-1": {ID: "SURFACE-1", Title: "implement-captain-ps", Workspace: "gavel-claude"}},
	)
	defer restore()

	result, err := RunPS(context.Background(), PSOptions{Source: "claude"})
	if err != nil {
		t.Fatalf("RunPS: %v", err)
	}
	if result.Total != 1 || len(result.Sessions) != 1 {
		t.Fatalf("sessions = %+v", result)
	}
	rec := result.Sessions[0]
	if rec.Live == nil {
		t.Fatal("expected live process")
	}
	if rec.Live.PID != 4242 || rec.Live.SessionID != "sess-ps" {
		t.Fatalf("live = %+v", rec.Live)
	}
	if rec.Live.SessionFile != root {
		t.Fatalf("primary session file = %q, want %q", rec.Live.SessionFile, root)
	}
	if len(rec.Live.AgentIDs) != 1 || rec.Live.AgentIDs[0] != "abc123" {
		t.Fatalf("agent ids = %v", rec.Live.AgentIDs)
	}
	if rec.Live.Surface == nil || rec.Live.Surface.SurfaceID != "SURFACE-1" {
		t.Fatalf("surface = %+v", rec.Live.Surface)
	}
	if rec.Live.Surface.Title != "implement-captain-ps" || rec.Live.Surface.Workspace != "gavel-claude" {
		t.Fatalf("cmux enrichment = %+v", rec.Live.Surface)
	}
	if got := psTitle(rec); got != "implement-captain-ps" {
		t.Fatalf("title = %q, want cmux surface title", got)
	}
	if rec.Live.LastActivity == nil {
		t.Fatal("expected last activity")
	}
	if rec.ID != "sess-ps" {
		t.Fatalf("record id = %q", rec.ID)
	}
	if result.Live != 1 || result.Active != 1 {
		t.Fatalf("summary = live %d active %d", result.Live, result.Active)
	}
}

// TestRunPSSynthesizesWhenNoTranscript covers a codex process with no resolvable
// open transcript: it still appears as a live record keyed by pid, with the CMUX
// surface attached.
func TestRunPSSynthesizesWhenNoTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)

	surface := &CmuxSurface{WorkspaceID: "WS-9"}
	restore := stubPSDiscovery(t,
		func() ([]agentProcess, error) {
			return []agentProcess{{Source: "codex", PID: 777, Status: "sleeping", Active: true, Command: "codex"}}, nil
		},
		func(pids []int) map[int][]string { return map[int][]string{} },
		func(pid int) *CmuxSurface { return surface },
		nil,
	)
	defer restore()

	result, err := RunPS(context.Background(), PSOptions{Source: "codex", All: true})
	if err != nil {
		t.Fatalf("RunPS: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("sessions = %+v", result)
	}
	rec := result.Sessions[0]
	if rec.ID != "pid:777" {
		t.Fatalf("record id = %q, want pid:777", rec.ID)
	}
	if rec.Live == nil || rec.Live.Surface == nil || rec.Live.Surface.WorkspaceID != "WS-9" {
		t.Fatalf("live = %+v", rec.Live)
	}
}

// TestRunPSRejectsForeignTranscript covers the claude mis-attribution fix: a
// claude process that holds only a FOREIGN project's transcript open (an
// inherited fd) must resolve to its OWN cwd's session, not the foreign one.
func TestRunPSRejectsForeignTranscript(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	projectA := filepath.Join(home, "work", "captain")
	projectB := filepath.Join(home, "work", "xero-cli")
	if err := os.MkdirAll(projectA, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(projectA)

	// The process's real session lives under projectA.
	own := filepath.Join(home, ".claude", "projects", claude.NormalizePath(projectA), "own-session.jsonl")
	writeJSONL(t, own, map[string]any{
		"type": "user", "sessionId": "own-session", "timestamp": "2026-06-01T10:00:00Z", "cwd": projectA,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "hi"}}},
	})
	// A foreign transcript the process merely holds open (inherited fd).
	foreign := filepath.Join(home, ".claude", "projects", claude.NormalizePath(projectB), "foreign-session.jsonl")
	writeJSONL(t, foreign, map[string]any{
		"type": "user", "sessionId": "foreign-session", "timestamp": "2026-06-01T09:00:00Z", "cwd": projectB,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "other"}}},
	})

	restore := stubPSDiscovery(t,
		func() ([]agentProcess, error) {
			return []agentProcess{{Source: "claude", PID: 4243, Status: "active", Active: true, CWD: projectA, Command: "claude"}}, nil
		},
		func(pids []int) map[int][]string { return map[int][]string{4243: {foreign}} },
		func(pid int) *CmuxSurface { return nil },
		nil,
	)
	defer restore()

	result, err := RunPS(context.Background(), PSOptions{Source: "claude"})
	if err != nil {
		t.Fatalf("RunPS: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("sessions = %+v", result)
	}
	rec := result.Sessions[0]
	if rec.Live == nil || rec.Live.SessionID != "own-session" {
		t.Fatalf("resolved session = %+v (must reject foreign fd, use own cwd)", rec.Live)
	}
	if rec.Live.SessionFile != own {
		t.Fatalf("session file = %q, want %q", rec.Live.SessionFile, own)
	}
}

// TestRunPSPrefersArgvSessionID covers the priority chain: a claude process
// whose argv carries --session-id must resolve to that session, even when a
// newer transcript exists under the same cwd (which cwd-newest would pick).
func TestRunPSPrefersArgvSessionID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := filepath.Join(home, "work", "captain")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	dir := filepath.Join(home, ".claude", "projects", claude.NormalizePath(project))
	argvSession := filepath.Join(dir, "argv-sid.jsonl")
	newerSession := filepath.Join(dir, "newer-sid.jsonl")
	writeJSONL(t, argvSession, map[string]any{
		"type": "user", "sessionId": "argv-sid", "timestamp": "2026-06-01T09:00:00Z", "cwd": project,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "argv"}}},
	})
	writeJSONL(t, newerSession, map[string]any{
		"type": "user", "sessionId": "newer-sid", "timestamp": "2026-06-01T11:00:00Z", "cwd": project,
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "newer"}}},
	})
	// Force newer-sid to be the most-recently-modified transcript.
	old := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC)
	if err := os.Chtimes(argvSession, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(newerSession, recent, recent); err != nil {
		t.Fatal(err)
	}

	restore := stubPSDiscovery(t,
		func() ([]agentProcess, error) {
			return []agentProcess{{Source: "claude", PID: 4244, Status: "active", Active: true, CWD: project,
				Command: "claude --session-id argv-sid"}}, nil
		},
		func(pids []int) map[int][]string { return map[int][]string{} },
		func(pid int) *CmuxSurface { return nil },
		nil,
	)
	defer restore()

	result, err := RunPS(context.Background(), PSOptions{Source: "claude"})
	if err != nil {
		t.Fatalf("RunPS: %v", err)
	}
	if result.Total != 1 {
		t.Fatalf("sessions = %+v", result)
	}
	rec := result.Sessions[0]
	if rec.Live == nil || rec.Live.SessionID != "argv-sid" {
		t.Fatalf("resolved = %+v, want argv-sid (argv id must beat cwd-newest)", rec.Live)
	}
	if rec.Live.SessionFile != argvSession {
		t.Fatalf("session file = %q, want %q", rec.Live.SessionFile, argvSession)
	}
}

func stubPSDiscovery(
	t *testing.T,
	procs func() ([]agentProcess, error),
	files func([]int) map[int][]string,
	surface func(int) *CmuxSurface,
	cmuxSurfaces map[string]cmux.Surface,
) func() {
	t.Helper()
	origProcs := discoverSessionProcesses
	origFiles := discoverOpenSessionFiles
	origSurface := discoverProcessSurface
	origCmux := discoverCmuxSurfaces
	discoverSessionProcesses = procs
	discoverOpenSessionFiles = files
	discoverProcessSurface = surface
	discoverCmuxSurfaces = func() (map[string]cmux.Surface, error) { return cmuxSurfaces, nil }
	return func() {
		discoverSessionProcesses = origProcs
		discoverOpenSessionFiles = origFiles
		discoverProcessSurface = origSurface
		discoverCmuxSurfaces = origCmux
	}
}
