// A whole run's transcripts: the environment sends the files its agent writes as they change,
// and the caller keeps them where that agent would keep them on the caller's machine.
package substraterun

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/promptrun/remoterun"
	"github.com/flanksource/commons/logger"
)

// transcriptRoots are where each agent writes its transcripts, under the home directory.
var transcriptRoots = map[string]string{
	"claude": filepath.Join(".claude", "projects"),
	"codex":  filepath.Join(".codex", "sessions"),
}

// transcriptInterval is how often the environment sends the transcripts that changed.
const transcriptInterval = 10 * time.Second

// transcriptSender sends the transcripts under home that changed since it last looked.
type transcriptSender struct {
	home string
	send func(remoterun.Message) error
	seen map[string]transcriptStamp
}

type transcriptStamp struct {
	size    int64
	modTime time.Time
}

// sync sends every transcript that changed since the last sync.
func (s *transcriptSender) sync() error {
	if s.seen == nil {
		s.seen = map[string]transcriptStamp{}
	}
	for _, source := range slices.Sorted(maps.Keys(transcriptRoots)) {
		dir := filepath.Join(s.home, transcriptRoots[source])
		err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return filepath.SkipDir
				}
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".jsonl" {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			stamp := transcriptStamp{size: info.Size(), modTime: info.ModTime()}
			if s.seen[path] == stamp {
				return nil
			}
			name, err := filepath.Rel(dir, path)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if err := s.send(remoterun.TranscriptMessage(source, filepath.ToSlash(name), data)); err != nil {
				return err
			}
			s.seen[path] = stamp
			return nil
		})
		if err != nil {
			return fmt.Errorf("send the %s transcripts: %w", source, err)
		}
	}
	return nil
}

// every syncs s each interval until ctx ends, writing what it couldn't send to log.
func (s *transcriptSender) every(ctx context.Context, interval time.Duration, log io.Writer) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.sync(); err != nil && ctx.Err() == nil {
				fmt.Fprintln(log, err)
			}
		}
	}
}

// transcriptPath is where a transcript the environment sent goes under home: name under
// source's transcript directory. A source captain doesn't know, or a name that isn't a relative
// .jsonl path inside that directory, is refused.
func transcriptPath(home, source, name string) (string, error) {
	root, ok := transcriptRoots[source]
	if !ok {
		return "", fmt.Errorf("transcript source %q is not claude or codex", source)
	}
	// No ".." at all, even one that cleans away: the name is joined under the agent's directory.
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "..") || filepath.Ext(name) != ".jsonl" {
		return "", fmt.Errorf("transcript name %q must be a relative .jsonl path inside %s's transcripts", name, source)
	}
	return filepath.Join(home, root, filepath.Clean(name)), nil
}

// keepTranscripts keeps a whole run's transcripts under home, where the agent would keep them
// had it run here, so captain's monitor finds them as it finds a local run's. One it can't keep
// is a warning: the run stands.
func keepTranscripts(home, run string) func(source, name string, data []byte) {
	return func(source, name string, data []byte) {
		path, err := transcriptPath(home, source, name)
		if err == nil {
			err = writeAtomically(path, data)
		}
		if err != nil {
			logger.Warnf("substrate run %s sent a transcript that couldn't be kept: %v", run, err)
		}
	}
}

// writeAtomically writes data to path through a temporary file beside it, so a reader never sees
// half a transcript.
func writeAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".transcript-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
