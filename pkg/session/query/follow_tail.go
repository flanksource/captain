package query

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/session"
	"github.com/flanksource/captain/pkg/session/transcript"
	"github.com/flanksource/commons/logger"
	"github.com/fsnotify/fsnotify"
)

// transcriptTail watches the provider log and its sub-agent transcripts using
// the same parser as the initial session read.
//
// The log's directory may not exist before the provider's first write, so the
// nearest existing ancestor is watched until the directory appears.
type transcriptTail struct {
	path    string
	source  string
	watcher *fsnotify.Watcher
	watched map[string]bool
	signal  chan struct{}
	errs    chan error
	done    chan struct{}
}

func startTranscriptTail(path, source string) (*transcriptTail, error) {
	if !api.IsTranscriptSource(source) {
		return nil, fmt.Errorf("cannot tail a %q transcript", source)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("watch transcript: %w", err)
	}
	tail := &transcriptTail{
		path: path, source: source, watcher: watcher, watched: make(map[string]bool),
		signal: make(chan struct{}, 1), errs: make(chan error, 1), done: make(chan struct{}),
	}
	if err := tail.watchNearest(); err != nil {
		return nil, errors.Join(err, watcher.Close())
	}
	go tail.loop()
	return tail, nil
}

// watchNearest follows directories as the provider creates its transcript tree.
func (t *transcriptTail) watchNearest() error {
	dirs := []string{filepath.Dir(t.path)}
	if t.source == "claude" {
		dirs = append(dirs, t.agentDir())
	}
	wanted := make(map[string]bool)
	for _, dir := range dirs {
		for {
			info, err := os.Stat(dir)
			if err == nil {
				if !info.IsDir() {
					return fmt.Errorf("transcript directory %s is not a directory", dir)
				}
				wanted[dir] = true
				break
			}
			if !os.IsNotExist(err) {
				return fmt.Errorf("stat %s: %w", dir, err)
			}
			if filepath.Dir(dir) == dir {
				return fmt.Errorf("no existing directory above %s", t.path)
			}
			dir = filepath.Dir(dir)
		}
	}
	for dir := range t.watched {
		if !wanted[dir] {
			if err := t.watcher.Remove(dir); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
				return fmt.Errorf("unwatch %s: %w", dir, err)
			}
			delete(t.watched, dir)
		}
	}
	for dir := range wanted {
		if !t.watched[dir] {
			if err := t.watcher.Add(dir); err != nil {
				return fmt.Errorf("watch %s: %w", dir, err)
			}
			t.watched[dir] = true
		}
	}
	return nil
}

func (t *transcriptTail) agentDir() string {
	return filepath.Join(strings.TrimSuffix(t.path, filepath.Ext(t.path)), "subagents")
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
	if event.Name == t.path || (t.source == "claude" && isAncestorDir(t.agentDir(), event.Name)) {
		t.wake()
		return nil
	}
	if isAncestorDir(event.Name, t.path) || (t.source == "claude" && (event.Name == t.agentDir() || isAncestorDir(event.Name, t.agentDir()))) {
		if err := t.watchNearest(); err != nil {
			return err
		}
		t.wake()
	}
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

// messages parses the transcript tree into conversational messages; synthetic
// event rows (ai-title and the like) carry no id to key an upsert by. A log
// that does not exist yet, or holds no conversation entry yet, has no
// messages; any other parse failure is an error.
func (t *transcriptTail) messages() ([]session.Message, error) {
	if _, err := os.Stat(t.path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat transcript %s: %w", t.path, err)
	}
	var parsed *session.Session
	var err error
	switch api.TranscriptFormat(t.source) {
	case "claude":
		parsed, err = buildClaudeTail(t.path, t.source)
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
	messages := make([]session.Message, 0, len(parsed.Messages))
	for _, message := range parsed.Messages {
		if session.IsConversationalMessage(message) {
			messages = append(messages, message)
		}
	}
	return messages, nil
}

// buildClaudeTail builds a growing Claude log. BuildTranscriptFile refuses a
// log with no entries, which is how every log starts, so that state is checked
// first rather than read back out of its error.
func buildClaudeTail(path, source string) (*session.Session, error) {
	entries, err := claude.ReadHistoryFileWithOptions(path, claude.ReadOptions{})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, nil
	}
	return transcript.Parse(transcript.Candidate{Source: source, Path: path})
}
