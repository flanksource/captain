package cli

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sync"

	"github.com/flanksource/captain/pkg/session"
	rpchttp "github.com/flanksource/clicky/rpc/http"
)

// sessionFileRef identifies a session file and its source for summarization.
type sessionFileRef struct {
	source string
	path   string
}

// summarizeSessionFileCached returns the persisted summary for a session file,
// reusing the stored row when the file's mtime+size are unchanged and otherwise
// rebuilding the rich rows (root + sub-agents) and upserting them. When the
// store is unavailable it degrades to building the summary uncached.
func summarizeSessionFileCached(ref sessionFileRef) (SessionRecord, error) {
	info, err := os.Stat(ref.path)
	if err != nil {
		return SessionRecord{}, err
	}
	if info.IsDir() {
		return SessionRecord{}, fmt.Errorf("%s is a directory", ref.path)
	}
	modUnix, size := info.ModTime().UnixNano(), info.Size()

	st := sessionStore()
	if st != nil {
		if row, ok := st.lookupFresh(ref.path, modUnix, size); ok {
			return row.toRecord(), nil
		}
	}

	rows, err := session.RowsFromFile(ref.path, ref.source)
	if err != nil {
		return SessionRecord{}, err
	}
	if st != nil {
		st.upsertRows(rows)
	}
	for _, r := range rows {
		if r.Path == ref.path {
			return recordFromRow(r), nil
		}
	}
	if len(rows) > 0 {
		return recordFromRow(rows[0]), nil
	}
	return SessionRecord{}, fmt.Errorf("no summary for %s", ref.path)
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
