package monitor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
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
	roots, agents := m.transcriptScanSet(ctx)
	ingestChanged(ctx, ingestor, roots)
	ingestChanged(ctx, ingestor, agents)
	// Remote task history rides the same pass: it is cheap when no mailbox
	// exists, and live task views read the mailbox directly rather than the
	// database, so this cadence only bounds how stale *history* can be.
	m.ingestGitAgentTasks(ctx)
}

// transcriptScanSet is everything one recon pass considers: what the filesystem
// offers, plus what has been registered by session id and the filesystem did not.
func (m *Monitor) transcriptScanSet(ctx context.Context) (roots, agents []transcriptRef) {
	roots, agents = discoverTranscripts()
	return append(roots, m.registeredTranscripts(ctx, roots, agents)...), agents
}

// registeredTranscripts are the transcripts some writer bound to a session by
// id (RegisterTranscriptSource) that filesystem discovery did not turn up.
// Discovery globs the agent's project directories, which is a snapshot of where
// agents have run; a run in a git worktree, or one whose project directory the
// ephemeral filter drops, is a session someone is waiting on that the glob never
// offers. They are scanned as roots because a registration names the session's
// own transcript, never a sub-agent's.
func (m *Monitor) registeredTranscripts(ctx context.Context, discovered ...[]transcriptRef) []transcriptRef {
	sources, err := m.db.ListSessionSources(ctx)
	if err != nil {
		log.Warnf("list registered transcript sources: %v", err)
		return nil
	}
	seen := make(map[string]struct{})
	for _, refs := range discovered {
		for _, ref := range refs {
			seen[ref.path] = struct{}{}
		}
	}
	extra := make([]transcriptRef, 0)
	for path, state := range sources {
		if _, known := seen[path]; known || strings.TrimSpace(state.SourceKind) == "" {
			continue
		}
		extra = append(extra, transcriptRef{source: state.SourceKind, path: path})
	}
	slices.SortFunc(extra, func(left, right transcriptRef) int { return strings.Compare(left.path, right.path) })
	return extra
}

func discoverTranscripts() (roots, agents []transcriptRef) {
	projectsDir := claude.GetProjectsDir()
	if rootFiles, err := claude.FindSessionFiles(projectsDir, "", true); err == nil {
		for _, path := range rootFiles {
			if isEphemeralClaudeTranscript(projectsDir, path) {
				continue
			}
			roots = append(roots, transcriptRef{source: "claude", path: path})
		}
	} else {
		log.Warnf("discover claude transcripts: %v", err)
	}
	if codexFiles, err := history.FindCodexSessionFiles(); err == nil {
		for _, path := range codexFiles {
			if ignored, _ := history.IsCodexAutoReviewSession(path); ignored {
				continue
			}
			roots = append(roots, transcriptRef{source: "codex", path: path})
		}
	} else {
		log.Warnf("discover codex transcripts: %v", err)
	}
	if agentFiles, err := claude.FindAgentTranscripts(projectsDir, "", true); err == nil {
		for _, path := range agentFiles {
			if isEphemeralClaudeTranscript(projectsDir, path) {
				continue
			}
			agents = append(agents, transcriptRef{source: "claude", path: path})
		}
	} else {
		log.Warnf("discover claude agent transcripts: %v", err)
	}
	return roots, agents
}

// isEphemeralClaudeTranscript excludes projects created below a system temp
// root whose working directory is already gone. Go integration tests that
// launch Claude leave their transcript mirror under ~/.claude/projects even
// after the temporary working directory is removed; those stale fixtures are
// not durable user sessions and should not be backfilled. A transcript whose
// temp working directory still exists (e.g. a test that is currently running)
// is a real, listable session and must be kept.
func isEphemeralClaudeTranscript(projectsDir, path string) bool {
	rel, err := filepath.Rel(projectsDir, path)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	projectDir := strings.SplitN(rel, string(filepath.Separator), 2)[0]
	for _, root := range []string{os.TempDir(), "/tmp", "/private/tmp"} {
		root = filepath.Clean(root)
		prefix := strings.TrimSuffix(claude.NormalizePath(root), "-")
		if prefix != "" && (projectDir == prefix || strings.HasPrefix(projectDir, prefix+"-")) {
			return !normalizedDirectoryExists(root, projectDir)
		}
	}
	return false
}

func normalizedDirectoryExists(root, target string) bool {
	normalizedRoot := claude.NormalizePath(root)
	if normalizedRoot == target {
		return true
	}
	if !strings.HasPrefix(target, normalizedRoot+"-") {
		return false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if normalizedDirectoryExists(path, target) {
			return true
		}
	}
	return false
}

func ingestChanged(ctx context.Context, ingestor *ingestor, refs []transcriptRef) {
	changed := make([]transcriptRef, 0)
	for _, ref := range refs {
		info, err := os.Stat(ref.path)
		if err != nil {
			continue
		}
		// This is where a scan decides to do nothing, so it is the only place
		// the skip rate can be observed: considered against ingested says
		// whether the bookkeeping is holding transcripts back at all.
		ingestor.monitor.ingest.filesConsidered.Add(1)
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
queueing:
	for _, ref := range changed {
		select {
		case <-ctx.Done():
			break queueing
		case queue <- ref:
		}
	}
	close(queue)
	wg.Wait()
}
