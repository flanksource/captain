package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"

	rpchttp "github.com/flanksource/clicky/rpc/http"
)

// sessionFileRef identifies a session file and its source for summarization.
type sessionFileRef struct {
	source string
	path   string
}

// summaryEntry is a cached sampled summary keyed by the file's identity at read
// time; a changed mtime or size invalidates it.
type summaryEntry struct {
	modUnix int64
	size    int64
	record  SessionRecord
}

// summaryCache memoizes sampled session summaries across requests (path →
// summaryEntry). Session files are append-only and there are at most a few
// thousand of them, so the cache is left unbounded — a changed mtime/size
// re-summarizes the one file that moved. It makes the dashboard's 5s poll and
// repeated listings effectively free after the first scan.
var summaryCache sync.Map

// summarizeSessionFileCached returns the sampled summary for a session file,
// reusing a cached result when the file's mtime and size are unchanged. The
// stat is one syscall versus reading the file, so a cache hit skips the read.
func summarizeSessionFileCached(ref sessionFileRef) (SessionRecord, error) {
	info, err := os.Stat(ref.path)
	if err != nil {
		return SessionRecord{}, err
	}
	if info.IsDir() {
		return SessionRecord{}, fmt.Errorf("%s is a directory", ref.path)
	}
	modUnix, size := info.ModTime().UnixNano(), info.Size()
	if cached, ok := summaryCache.Load(ref.path); ok {
		entry := cached.(summaryEntry)
		if entry.modUnix == modUnix && entry.size == size {
			return entry.record, nil
		}
	}
	record, err := summarizeSessionFileFast(ref.source, ref.path)
	if err != nil {
		return SessionRecord{}, err
	}
	summaryCache.Store(ref.path, summaryEntry{modUnix: modUnix, size: size, record: record})
	return record, nil
}

func summarizeSessionFileFast(source, path string) (SessionRecord, error) {
	switch source {
	case "claude":
		return summarizeClaudeSessionFileFast(path)
	case "codex":
		return summarizeCodexSessionFileFast(path)
	default:
		return SessionRecord{}, fmt.Errorf("unknown session source %q", source)
	}
}

// summarizeSessionRefs sampled-summarizes files concurrently (bounded by
// GOMAXPROCS) through the summary cache, preserving input order. Files that
// fail to summarize or yield no session id are dropped. It records the "parse"
// phase for the wall time of the whole batch.
func summarizeSessionRefs(ctx context.Context, refs []sessionFileRef) []sessionCandidate {
	defer rpchttp.Track(ctx, "parse")()

	records := make([]SessionRecord, len(refs))
	found := make([]bool, len(refs))

	workers := runtime.GOMAXPROCS(0)
	if workers > len(refs) {
		workers = len(refs)
	}
	if workers < 1 {
		workers = 1
	}

	jobs := make(chan int)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				record, err := summarizeSessionFileCached(refs[i])
				if err != nil || record.ID == "" {
					continue
				}
				records[i] = record
				found[i] = true
			}
		}()
	}
	for i := range refs {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	candidates := make([]sessionCandidate, 0, len(refs))
	for i := range refs {
		if found[i] {
			candidates = append(candidates, sessionCandidate{record: records[i], path: refs[i].path})
		}
	}
	return candidates
}
