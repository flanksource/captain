package monitor

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// transcriptWatcher tails live transcripts: it watches directories (per-file
// watches break across rename/recreate), filters events to JSONL transcripts,
// and debounces bursts of appends into one ingest per quiet period.
type transcriptWatcher struct {
	monitor *Monitor
	watcher *fsnotify.Watcher
	// ingest runs after the debounce window; ingestor.ingestFile in production.
	ingest func(ctx context.Context, source, path string)

	mu       sync.Mutex
	dirs     map[string]string // watched directory -> source kind
	tracked  map[string]string // explicitly tracked file -> source kind
	timers   map[string]*time.Timer
	debounce time.Duration
}

func newTranscriptWatcher(m *Monitor, ingestor *ingestor) (*transcriptWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &transcriptWatcher{
		monitor: m, watcher: watcher,
		dirs: map[string]string{}, tracked: map[string]string{}, timers: map[string]*time.Timer{},
		debounce: m.cfg.Debounce,
	}
	w.ingest = func(ctx context.Context, source, path string) {
		if err := ingestor.ingestFile(ctx, source, path); err != nil {
			log.Warnf("ingest %s: %v", path, err)
		}
	}
	ingestor.watchSubagents = func(rootTranscriptPath string) {
		w.watchDir(subagentsDir(rootTranscriptPath), "claude")
	}
	ingestor.requeue = w.schedule
	return w, nil
}

func (w *transcriptWatcher) events() chan fsnotify.Event { return w.watcher.Events }
func (w *transcriptWatcher) errors() chan error          { return w.watcher.Errors }

func (w *transcriptWatcher) close() {
	w.mu.Lock()
	for _, timer := range w.timers {
		timer.Stop()
	}
	w.timers = map[string]*time.Timer{}
	w.mu.Unlock()
	_ = w.watcher.Close()
}

// track registers one transcript file and watches its directory (and, for
// claude root transcripts, the session's subagents directory when it appears).
func (w *transcriptWatcher) track(path, source string) {
	if path == "" {
		return
	}
	w.mu.Lock()
	_, known := w.tracked[path]
	w.tracked[path] = source
	w.mu.Unlock()
	if !known {
		w.watchDir(filepath.Dir(path), source)
	}
}

func (w *transcriptWatcher) watchDir(dir, source string) {
	if dir == "" {
		return
	}
	w.mu.Lock()
	_, known := w.dirs[dir]
	if !known {
		w.dirs[dir] = source
	}
	w.mu.Unlock()
	if known {
		return
	}
	if err := w.watcher.Add(dir); err != nil {
		log.Debugf("watch %s: %v", dir, err)
		w.mu.Lock()
		delete(w.dirs, dir)
		w.mu.Unlock()
	}
}

// handle debounces one fsnotify event into a future ingest of the file.
func (w *transcriptWatcher) handle(ctx context.Context, event fsnotify.Event) {
	if !event.Op.Has(fsnotify.Write) && !event.Op.Has(fsnotify.Create) {
		return
	}
	path := event.Name
	source, ok := w.classify(path)
	if !ok {
		return
	}
	w.schedule(ctx, source, path)
}

func (w *transcriptWatcher) schedule(ctx context.Context, source, path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if timer, ok := w.timers[path]; ok {
		timer.Reset(w.debounce)
		return
	}
	w.timers[path] = time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		delete(w.timers, path)
		w.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		w.ingest(ctx, source, path)
	})
}

// classify decides whether an event path is a transcript worth ingesting and
// which source parser owns it: explicitly tracked files always qualify; other
// JSONL files qualify when they live in a watched directory.
func (w *transcriptWatcher) classify(path string) (string, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if source, ok := w.tracked[path]; ok {
		return source, true
	}
	if !strings.HasSuffix(path, ".jsonl") {
		return "", false
	}
	if source, ok := w.dirs[filepath.Dir(path)]; ok {
		return source, true
	}
	return "", false
}
