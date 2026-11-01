package monitor

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	"github.com/fsnotify/fsnotify"
)

func (w *transcriptWatcher) untrackLocked(path string) {
	delete(w.tracked, path)
	delete(w.processPaths, path)
	for pending, timer := range w.timers {
		if pending == path || strings.HasPrefix(pending, subagentsDir(path)+string(filepath.Separator)) {
			timer.Stop()
			delete(w.timers, pending)
		}
	}
}

func (w *transcriptWatcher) untrack(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.untrackLocked(path)
	if err := w.reconcileLocked(); err != nil {
		log.Errorf("untrack transcript %s: %v", path, err)
	}
}

// syncProcesses retires paths previously owned by an observed process, while
// preserving registrations whose process has not appeared in discovery yet.
func (w *transcriptWatcher) syncProcesses(paths, dirs map[string]string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return errors.New("sync transcript watches: watcher is closed")
	}
	for path := range w.processPaths {
		if _, alive := paths[path]; !alive {
			w.monitor.mu.Lock()
			delete(w.monitor.tracked, path)
			w.monitor.mu.Unlock()
			w.untrackLocked(path)
		}
	}
	w.processPaths, w.processDirs = paths, dirs
	w.tracked = maps.Clone(paths)
	maps.Copy(w.tracked, w.monitor.trackedPaths())
	return w.reconcileLocked()
}

// Directory watches open a descriptor for each child on macOS. Keep only
// discovery directories and the transcript/subagent directories still in use.
func (w *transcriptWatcher) reconcileLocked() error {
	wanted := maps.Clone(w.processDirs)
	if wanted == nil {
		wanted = map[string]string{}
	}
	for path, source := range w.tracked {
		wanted[filepath.Dir(path)] = source
		if source == "claude" {
			wanted[filepath.Dir(subagentsDir(path))] = source
			wanted[subagentsDir(path)] = source
		}
	}
	for dir := range w.dirs {
		if _, keep := wanted[dir]; keep {
			continue
		}
		if err := w.watcher.Remove(dir); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
			return fmt.Errorf("unwatch %s: %w", dir, err)
		}
		delete(w.dirs, dir)
	}
	for dir, source := range wanted {
		if _, watched := w.dirs[dir]; watched {
			continue
		}
		if err := w.watcher.Add(dir); err != nil {
			if os.IsNotExist(err) {
				continue // The provider may create this directory on its next write.
			}
			return fmt.Errorf("watch %s: %w", dir, err)
		}
		w.dirs[dir] = source
	}
	return nil
}
