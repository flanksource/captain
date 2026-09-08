package monitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/database"
	"github.com/fsnotify/fsnotify"
	"github.com/google/uuid"
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
	registerActiveWatcher(w)
	return w, nil
}

// activeWatchers is every transcript watcher running in this process. A watcher
// exists only while a monitor holds the writer lock, and RegisterTranscriptSource
// hands newly bound transcripts to whichever one that is — the alternative is
// waiting out the process poll or the daily recon for a project directory that
// was created seconds ago.
var activeWatchers = struct {
	mu  sync.Mutex
	set map[*transcriptWatcher]struct{}
}{set: map[*transcriptWatcher]struct{}{}}

func registerActiveWatcher(w *transcriptWatcher) {
	activeWatchers.mu.Lock()
	activeWatchers.set[w] = struct{}{}
	activeWatchers.mu.Unlock()
}

func unregisterActiveWatcher(w *transcriptWatcher) {
	activeWatchers.mu.Lock()
	delete(activeWatchers.set, w)
	activeWatchers.mu.Unlock()
}

func (w *transcriptWatcher) events() chan fsnotify.Event { return w.watcher.Events }
func (w *transcriptWatcher) errors() chan error          { return w.watcher.Errors }

func (w *transcriptWatcher) close() {
	unregisterActiveWatcher(w)
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

// ErrTranscriptNotFound reports that a provider session id names no transcript
// on disk yet. Registration happens the moment the id becomes known, which can
// be before the agent has flushed its first line, so a caller reads this as
// "not yet" rather than as a failure.
var ErrTranscriptNotFound = errors.New("no transcript file for provider session")

// RegisterTranscriptSource binds a provider session id to the transcript file it
// writes and arms this process's monitor on it.
//
// Discovery is otherwise shaped by the working directory — an adaptive ps poll
// plus a scan of the agent's project directories — so a run inside a fresh git
// worktree writes into a directory that did not exist when the last scan ran and
// that no live process names, and its transcript waits for the daily recon.
// Provider session ids are the one handle that exists at run start and that the
// agent keys its log by, so resolution here is by id and needs no cwd at all.
func RegisterTranscriptSource(ctx context.Context, db *database.DB, sessionID uuid.UUID, providerSessionID, source string) (string, error) {
	if db == nil {
		return "", fmt.Errorf("register transcript source: a database is required")
	}
	if sessionID == uuid.Nil {
		return "", fmt.Errorf("register transcript source: session ID is required")
	}
	source = strings.TrimSpace(source)
	providerSessionID = strings.TrimSpace(providerSessionID)
	path, err := findTranscriptPath(source, providerSessionID)
	if err != nil {
		return "", err
	}
	if err := db.RegisterTranscriptSource(ctx, sessionID, source, path, providerSessionID); err != nil {
		return "", err
	}
	armActiveWatchers(ctx, source, path)
	return path, nil
}

// findTranscriptPath resolves a provider session id to the transcript file the
// agent writes for it, without knowing the working directory the agent ran in.
func findTranscriptPath(source, providerSessionID string) (string, error) {
	source, providerSessionID = strings.TrimSpace(source), strings.TrimSpace(providerSessionID)
	if providerSessionID == "" {
		return "", fmt.Errorf("resolve transcript: provider session ID is required")
	}
	switch source {
	case "claude":
		path, err := history.FindSessionFile(providerSessionID)
		if err != nil {
			return "", fmt.Errorf("%w: claude session %s: %w", ErrTranscriptNotFound, providerSessionID, err)
		}
		return path, nil
	case "codex":
		return findCodexRollout(providerSessionID)
	default:
		return "", fmt.Errorf("resolve transcript: unknown transcript source %q", source)
	}
}

// findCodexRollout locates the rollout file whose name ends in the session id.
// Codex names a rollout by its start time and its id, so the id is the stable
// half; the newest match wins when a session was rolled over more than once.
func findCodexRollout(providerSessionID string) (string, error) {
	files, err := history.FindCodexSessionFiles()
	if err != nil {
		return "", fmt.Errorf("resolve codex session %s: %w", providerSessionID, err)
	}
	newest, newestMod := "", int64(-1)
	for _, file := range files {
		base := strings.TrimSuffix(filepath.Base(file), ".jsonl")
		if base != providerSessionID && !strings.HasSuffix(base, "-"+providerSessionID) {
			continue
		}
		info, statErr := os.Stat(file)
		if statErr != nil {
			continue
		}
		if mod := info.ModTime().UnixNano(); mod > newestMod {
			newest, newestMod = file, mod
		}
	}
	if newest == "" {
		return "", fmt.Errorf("%w: codex session %s", ErrTranscriptNotFound, providerSessionID)
	}
	return newest, nil
}

// armActiveWatchers hands a transcript to every watcher running in this process:
// the directory, so later appends are tailed, and one scheduled ingest, so a file
// already written in full is not left for the daily recon.
//
// The scheduled ingest deliberately outlives the caller's context. Registration
// happens on a run-start report or an HTTP request whose context is cancelled
// long before the agent stops writing, and cancelling the ingest with it would
// make the fast path the one that reliably does nothing.
func armActiveWatchers(ctx context.Context, source, path string) {
	ingestCtx := context.WithoutCancel(ctx)
	activeWatchers.mu.Lock()
	watchers := make([]*transcriptWatcher, 0, len(activeWatchers.set))
	for watcher := range activeWatchers.set {
		watchers = append(watchers, watcher)
	}
	activeWatchers.mu.Unlock()
	for _, watcher := range watchers {
		watcher.track(path, source)
		watcher.schedule(ingestCtx, source, path)
	}
}
