package query

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/commons/logger"
	"github.com/fsnotify/fsnotify"
)

// transcriptTail watches one provider log that has not been ingested yet. It
// signals on every write to the file and re-parses the whole file with the
// same parser ingest uses, so its messages carry the ids the database will.
//
// The log's directory may not exist before the provider's first write, so the
// nearest existing ancestor is watched until the directory appears.
type transcriptTail struct {
	path    string
	source  string
	watcher *fsnotify.Watcher
	watched string
	signal  chan struct{}
	errs    chan error
	done    chan struct{}
}

func startTranscriptTail(path, source string) (*transcriptTail, error) {
	if source != "claude" && source != "codex" {
		return nil, fmt.Errorf("cannot tail a %q transcript", source)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch transcript: %w", err)
	}
	tail := &transcriptTail{
		path: path, source: source, watcher: watcher,
		signal: make(chan struct{}, 1), errs: make(chan error, 1), done: make(chan struct{}),
	}
	if err := tail.watchNearest(); err != nil {
		return nil, errors.Join(err, watcher.Close())
	}
	go tail.loop()
	return tail, nil
}

// watchNearest moves the watch to the deepest existing directory on the way
// to the log.
func (t *transcriptTail) watchNearest() error {
	dir := filepath.Dir(t.path)
	for {
		info, err := os.Stat(dir)
		if err == nil && info.IsDir() {
			break
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("stat %s: %w", dir, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("no existing directory above %s", t.path)
		}
		dir = parent
	}
	if dir == t.watched {
		return nil
	}
	if t.watched != "" {
		if err := t.watcher.Remove(t.watched); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
			return fmt.Errorf("unwatch %s: %w", t.watched, err)
		}
	}
	if err := t.watcher.Add(dir); err != nil {
		return fmt.Errorf("watch %s: %w", dir, err)
	}
	t.watched = dir
	return nil
}

func (t *transcriptTail) loop() {
	for {
		select {
		case <-t.done:
			return
		case event, ok := <-t.watcher.Events:
			if !ok {
				return
			}
			if err := t.handle(event); err != nil {
				t.fail(err)
				return
			}
		case err, ok := <-t.watcher.Errors:
			if !ok {
				return
			}
			t.fail(fmt.Errorf("watch transcript %s: %w", t.path, err))
			return
		}
	}
}

func (t *transcriptTail) handle(event fsnotify.Event) error {
	if event.Name == t.path {
		t.wake()
		return nil
	}
	if !event.Op.Has(fsnotify.Create) || !isAncestorDir(event.Name, t.path) {
		return nil
	}
	// A directory on the way to the log appeared: descend, and look for the
	// log itself in case it was created before the watch moved.
	if err := t.watchNearest(); err != nil {
		return err
	}
	t.wake()
	return nil
}

func isAncestorDir(dir, path string) bool {
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

func (t *transcriptTail) wake() {
	select {
	case t.signal <- struct{}{}:
	default:
	}
}

func (t *transcriptTail) fail(err error) {
	select {
	case t.errs <- err:
	default:
	}
}

func (t *transcriptTail) close() {
	close(t.done)
	if err := t.watcher.Close(); err != nil {
		logger.Warnf("captain session follow: close transcript watch %s: %v", t.path, err)
	}
}

// messages parses the log. A log that does not exist yet, or holds no
// conversation entry yet, has no messages; any other parse failure is an error.
func (t *transcriptTail) messages() ([]session.Message, error) {
	if _, err := os.Stat(t.path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat transcript %s: %w", t.path, err)
	}
	var parsed *session.Session
	var err error
	switch t.source {
	case "claude":
		parsed, err = buildClaudeTail(t.path)
	case "codex":
		parsed, err = session.BuildCodexFile(t.path)
		if errors.Is(err, session.ErrCodexEmpty) {
			return nil, nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("parse %s transcript %s: %w", t.source, t.path, err)
	}
	if parsed == nil {
		return nil, nil
	}
	return parsed.Messages, nil
}

// buildClaudeTail builds a growing Claude log. BuildTranscriptFile refuses a
// log with no entries, which is how every log starts, so that state is checked
// first rather than read back out of its error.
func buildClaudeTail(path string) (*session.Session, error) {
	entries, err := claude.ReadHistoryFileWithOptions(path, claude.ReadOptions{})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	parsed, _, err := session.BuildTranscriptFile(path)
	return parsed, err
}
