package monitor

import (
	"context"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

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

func TestRequestBackfillCoalescesWhileWorkerIsBusy(t *testing.T) {
	requests := make(chan struct{}, 1)
	requestBackfill(requests)
	requestBackfill(requests)
	requestBackfill(requests)

	select {
	case <-requests:
	case <-time.After(time.Second):
		t.Fatal("backfill request was not queued")
	}
	select {
	case <-requests:
		t.Fatal("duplicate backfill requests were not coalesced")
	default:
	}
}

func TestMonitorReadyClosesOnce(t *testing.T) {
	m := &Monitor{ready: make(chan struct{})}
	m.markReady()
	m.markReady()

	select {
	case <-m.Ready():
	case <-time.After(time.Second):
		t.Fatal("monitor readiness was not signalled")
	}
}
