package monitor

import (
	"context"
	"os"
	"runtime"
	"sync"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/claude"
)

type transcriptRef struct {
	source string
	path   string
}

// backfill is the incremental scan over every known transcript: files whose
// mtime/size/parser version match their bookkeeping row are skipped, changed
// ones are re-ingested with a bounded worker pool. Root transcripts are
// ingested before agent transcripts so children always find their parent.
func (m *Monitor) backfill(ctx context.Context, ingestor *ingestor) {
	if err := ingestor.refreshSourceStates(ctx); err != nil {
		log.Warnf("refresh transcript bookkeeping: %v", err)
		return
	}
	roots, agents := discoverTranscripts()
	ingestChanged(ctx, ingestor, roots)
	ingestChanged(ctx, ingestor, agents)
}

func discoverTranscripts() (roots, agents []transcriptRef) {
	projectsDir := claude.GetProjectsDir()
	if rootFiles, err := claude.FindSessionFiles(projectsDir, "", true); err == nil {
		for _, path := range rootFiles {
			roots = append(roots, transcriptRef{source: "claude", path: path})
		}
	} else {
		log.Warnf("discover claude transcripts: %v", err)
	}
	if codexFiles, err := history.FindCodexSessionFiles(); err == nil {
		for _, path := range codexFiles {
			roots = append(roots, transcriptRef{source: "codex", path: path})
		}
	} else {
		log.Warnf("discover codex transcripts: %v", err)
	}
	if agentFiles, err := claude.FindAgentTranscripts(projectsDir, "", true); err == nil {
		for _, path := range agentFiles {
			agents = append(agents, transcriptRef{source: "claude", path: path})
		}
	} else {
		log.Warnf("discover claude agent transcripts: %v", err)
	}
	return roots, agents
}

func ingestChanged(ctx context.Context, ingestor *ingestor, refs []transcriptRef) {
	changed := make([]transcriptRef, 0)
	for _, ref := range refs {
		info, err := os.Stat(ref.path)
		if err != nil {
			continue
		}
		if ingestor.needsIngest(ref.path, info) {
			changed = append(changed, ref)
		}
	}
	if len(changed) == 0 {
		return
	}
	log.Infof("ingesting %d changed transcripts", len(changed))
	workers := runtime.GOMAXPROCS(0)
	if workers > len(changed) {
		workers = len(changed)
	}
	queue := make(chan transcriptRef)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ref := range queue {
				if ctx.Err() != nil {
					return
				}
				if err := ingestor.ingestFile(ctx, ref.source, ref.path); err != nil {
					log.Warnf("ingest %s: %v", ref.path, err)
				}
			}
		}()
	}
	for _, ref := range changed {
		if ctx.Err() != nil {
			break
		}
		queue <- ref
	}
	close(queue)
	wg.Wait()
}
