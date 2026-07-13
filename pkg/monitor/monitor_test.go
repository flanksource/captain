package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestParseAgentProcessLine(t *testing.T) {
	line := "4242 12.5  3.2 204800 S+   Sun Jul 12 09:00:00 2026 /usr/local/bin/claude --resume 0195c1de-4ab8-7000-8000-0123456789ab"
	proc, ok := parseAgentProcessLine(line)
	if !ok {
		t.Fatal("line should parse")
	}
	if proc.PID != 4242 || proc.Source != "claude" {
		t.Fatalf("pid=%d source=%q", proc.PID, proc.Source)
	}
	if proc.CPUPercent != 12.5 || proc.MemoryPercent != 3.2 {
		t.Fatalf("cpu=%v mem=%v", proc.CPUPercent, proc.MemoryPercent)
	}
	if proc.MemoryRSSKB != 204800 {
		t.Fatalf("rss=%d", proc.MemoryRSSKB)
	}
	if proc.Status != "sleeping" {
		t.Fatalf("status=%q", proc.Status)
	}
	if proc.StartedAt == nil || proc.StartedAt.Year() != 2026 {
		t.Fatalf("startedAt=%v", proc.StartedAt)
	}
	if got := parseClaudeSessionIDFromCommand(proc.Command); got != "0195c1de-4ab8-7000-8000-0123456789ab" {
		t.Fatalf("session id from command = %q", got)
	}
}

func TestParseAgentProcessLine_SkipsNonAgents(t *testing.T) {
	for _, line := range []string{
		"77 0.0 0.1 1024 S Sun Jul 12 09:00:00 2026 /usr/bin/captain serve",
		"78 0.0 0.1 1024 S Sun Jul 12 09:00:00 2026 /opt/codex/mcp-server --port 1",
		"79 0.0 0.1 1024 S Sun Jul 12 09:00:00 2026 /bin/zsh -l",
	} {
		if _, ok := parseAgentProcessLine(line); ok {
			t.Errorf("line should be skipped: %s", line)
		}
	}
}

// TestWatcherDebounce verifies a burst of write events produces one ingest
// after the quiet period rather than one per event.
func TestWatcherDebounce(t *testing.T) {
	m := &Monitor{cfg: Config{Debounce: 30 * time.Millisecond}, tracked: map[string]string{}}
	path := "/tmp/x/session.jsonl"
	fired := make(chan string, 10)
	w := &transcriptWatcher{
		monitor: m,
		dirs:    map[string]string{}, tracked: map[string]string{path: "claude"},
		timers: map[string]*time.Timer{}, debounce: m.cfg.Debounce,
		ingest: func(_ context.Context, _, p string) { fired <- p },
	}

	for range 5 {
		w.handle(t.Context(), fsnotify.Event{Name: path, Op: fsnotify.Write})
	}

	select {
	case got := <-fired:
		if got != path {
			t.Fatalf("ingested %q, want %q", got, path)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("debounced ingest never fired")
	}
	select {
	case <-fired:
		t.Fatal("burst fired more than once")
	case <-time.After(3 * w.debounce):
	}
}

func TestWatcherClassify(t *testing.T) {
	m := &Monitor{cfg: Config{Debounce: time.Millisecond}, tracked: map[string]string{}}
	w := &transcriptWatcher{
		monitor: m,
		dirs:    map[string]string{"/proj/-home-dev-example": "claude"},
		tracked: map[string]string{"/other/tracked.jsonl": "codex"},
		timers:  map[string]*time.Timer{},
	}
	cases := []struct {
		path   string
		source string
		ok     bool
	}{
		{"/other/tracked.jsonl", "codex", true},
		{"/proj/-home-dev-example/new-session.jsonl", "claude", true},
		{"/proj/-home-dev-example/notes.txt", "", false},
		{"/unwatched/dir/session.jsonl", "", false},
	}
	for _, c := range cases {
		source, ok := w.classify(c.path)
		if ok != c.ok || source != c.source {
			t.Errorf("classify(%s) = (%q, %v), want (%q, %v)", c.path, source, ok, c.source, c.ok)
		}
	}
}
