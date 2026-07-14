package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
)

// parserVersion invalidates every ingested transcript when the parsing or
// mapping logic changes shape.
const parserVersion = 2

// ingestor turns changed transcript files into native database rows, skipping
// files whose recorded mtime/size/parser version still match the disk state.
type ingestor struct {
	monitor *Monitor
	db      *database.DB
	// watchSubagents arms the watcher on a root transcript's subagents
	// directory; nil outside watched (one-shot) runs.
	watchSubagents func(rootTranscriptPath string)

	mu          sync.Mutex
	sources     map[string]database.SessionSourceState
	ingestLocks sync.Map // transcript path -> *sync.Mutex
}

func newIngestor(m *Monitor) *ingestor {
	return &ingestor{monitor: m, db: m.db, sources: map[string]database.SessionSourceState{}}
}

func (ing *ingestor) refreshSourceStates(ctx context.Context) error {
	ing.mu.Lock()
	defer ing.mu.Unlock()
	sources, err := ing.db.ListSessionSources(ctx)
	if err != nil {
		return err
	}
	ing.sources = sources
	return nil
}

func (ing *ingestor) sourceState(path string) (database.SessionSourceState, bool) {
	ing.mu.Lock()
	defer ing.mu.Unlock()
	state, ok := ing.sources[path]
	return state, ok
}

func (ing *ingestor) recordSourceState(state database.SessionSourceState) {
	ing.mu.Lock()
	ing.sources[state.Path] = state
	ing.mu.Unlock()
}

// needsIngest compares the on-disk stat with the recorded bookkeeping.
func (ing *ingestor) needsIngest(path string, info os.FileInfo) bool {
	state, ok := ing.sourceState(path)
	if !ok {
		return true
	}
	return state.ParserVersion != parserVersion ||
		state.ObservedSize != info.Size() ||
		state.ObservedModTime == nil ||
		!state.ObservedModTime.Equal(info.ModTime().UTC())
}

// ingestFile parses one transcript and persists it. Claude sub-agent files are
// child sessions of the root transcript's session; root ingests also arm the
// watcher on the session's subagents directory.
func (ing *ingestor) ingestFile(ctx context.Context, source, path string) error {
	lockValue, _ := ing.ingestLocks.LoadOrStore(path, &sync.Mutex{})
	pathLock := lockValue.(*sync.Mutex)
	pathLock.Lock()
	defer pathLock.Unlock()

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			ing.monitor.untrackTranscript(path)
			return nil
		}
		return err
	}
	if !ing.needsIngest(path, info) {
		return nil
	}
	if source == "codex" {
		ignored, ignoreErr := history.IsCodexAutoReviewSession(path)
		if ignoreErr != nil {
			return ignoreErr
		}
		if ignored {
			return nil
		}
	}
	var input database.IngestTranscriptInput
	switch source {
	case "claude":
		input, err = ing.claudeIngestInput(ctx, path)
	case "codex":
		input, err = codexIngestInput(path)
	default:
		return fmt.Errorf("unknown transcript source %q for %s", source, path)
	}
	if err != nil {
		return err
	}
	input.Session.HostID = ing.monitor.cfg.HostID
	input.Source.SourceKind = source
	input.Source.Path = path
	input.Source.ParserVersion = parserVersion
	input.Source.ObservedSize = info.Size()
	input.Source.ByteOffset = info.Size()
	input.Source.ObservedModTime = info.ModTime().UTC()

	persisted, err := ing.db.IngestTranscript(ctx, input)
	if err != nil {
		return err
	}
	if source == "claude" && input.Session.ParentSessionID == nil && ing.watchSubagents != nil {
		ing.watchSubagents(path)
	}
	modTime := input.Source.ObservedModTime
	ing.recordSourceState(database.SessionSourceState{
		SessionID: persisted.ID, SourceKind: source, Path: path, ParserVersion: parserVersion,
		ByteOffset: input.Source.ByteOffset, ObservedSize: input.Source.ObservedSize, ObservedModTime: &modTime,
	})
	return nil
}

func (ing *ingestor) claudeIngestInput(ctx context.Context, path string) (database.IngestTranscriptInput, error) {
	s, info, err := session.BuildTranscriptFile(path)
	if err != nil {
		return database.IngestTranscriptInput{}, err
	}
	input := unifiedIngestInput(s, "claude", claudeSequence)
	input.Source.SourceIdentity = info.RootSessionID
	if info.IsAgent {
		root, err := ing.db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: info.RootSessionID, Source: "claude", HostID: ing.monitor.cfg.HostID,
		})
		if err != nil {
			return database.IngestTranscriptInput{}, err
		}
		input.Session.ProviderSessionID = info.AgentID
		input.Session.ParentSessionID = &root.ID
		input.Session.AgentType = info.AgentType
		input.Session.Description = info.AgentDesc
		input.Session.Path = path
		input.Turns = nil // turns belong to the root session
	} else {
		input.Session.ProviderSessionID = info.RootSessionID
	}
	return input, nil
}

func codexIngestInput(path string) (database.IngestTranscriptInput, error) {
	sessions := session.BuildCodex([]string{path})
	if len(sessions) == 0 {
		return database.IngestTranscriptInput{}, fmt.Errorf("codex transcript %s is not parseable", path)
	}
	s := sessions[0]
	input := unifiedIngestInput(s, "codex", func(m session.Message, index int) int64 {
		return int64(index + 1)
	})
	input.Session.ProviderSessionID = s.ID
	input.Source.SourceIdentity = s.ID
	return input, nil
}

// claudeSequence keys messages by their transcript line: stable across
// re-parses and the seek reference the UI uses against the raw file.
func claudeSequence(m session.Message, _ int) int64 { return m.SourceLine }

// unifiedIngestInput maps the unified session model onto the native ingest
// batch: turns become turn rows with one aggregate model call each, and
// conversational messages become message rows keyed by sequence.
func unifiedIngestInput(s *session.Session, backend string, sequence func(session.Message, int) int64) database.IngestTranscriptInput {
	input := database.IngestTranscriptInput{
		Session: database.IngestSessionInput{
			Source: backend, Path: s.HistoryFile, Project: s.Project, CWD: s.CWD,
			Title: s.Title, InitialPrompt: s.InitialPrompt, Slug: s.Slug, CLIVersion: s.Version,
			StartedAt: s.StartedAt, LastActivityAt: s.EndedAt,
			Git:      gitMetadata(s),
			Metadata: sessionMetadata(s),
		},
	}
	turnIndexByID := map[string]int{}
	for _, turn := range s.Turns {
		turnIndexByID[turn.ID] = turn.Index
		ingestTurn := database.IngestTurn{
			Index: turn.Index, ProviderTurnID: turn.ID, Status: database.TurnStatusEnded,
			StopReason: turn.StopReason, StartedAt: turn.StartedAt, EndedAt: turn.EndedAt,
			Call: &database.IngestModelCall{
				Model: turn.Model, Backend: backend, Effort: turn.ReasoningEffort,
				InputTokens:  int64(turn.Usage.InputTokens),
				OutputTokens: int64(turn.Usage.OutputTokens), ReasoningTokens: int64(turn.Usage.ReasoningTokens),
				CacheReadTokens: int64(turn.Usage.CacheReadTokens), CacheWriteTokens: int64(turn.Usage.CacheWriteTokens),
				InputCost: turn.Cost.InputCost, OutputCost: turn.Cost.OutputCost,
				CacheReadCost: turn.Cost.CacheReadCost, CacheWriteCost: turn.Cost.CacheWriteCost,
				StartedAt: turn.StartedAt, EndedAt: turn.EndedAt,
			},
		}
		if turn.Context != nil {
			ingestTurn.Call.ContextTokens = int64(turn.Context.UsedTokens)
			ingestTurn.Call.ContextWindowTokens = int64(turn.Context.WindowTokens)
		}
		input.Turns = append(input.Turns, ingestTurn)
	}
	seen := map[int64]bool{}
	for i, m := range s.Messages {
		if !session.IsConversationalMessage(m) {
			continue
		}
		seq := sequence(m, i)
		if seq <= 0 || seen[seq] {
			continue
		}
		seen[seq] = true
		parts, err := json.Marshal(m.Parts)
		if err != nil {
			continue
		}
		message := database.IngestMessage{
			Sequence: seq, ProviderMessageID: m.ID, Role: m.Role, PartsJSON: parts, SourceLine: m.SourceLine,
		}
		if m.Provenance != nil {
			message.OccurredAt = m.Provenance.Timestamp
		}
		if index, ok := turnIndexByID[m.TurnID]; ok && m.TurnID != "" {
			turnIndex := index
			message.TurnIndex = &turnIndex
		}
		input.Messages = append(input.Messages, message)
	}
	return input
}

func gitMetadata(s *session.Session) map[string]any {
	if s.Git.Branch == "" && s.Git.Commit == "" && s.Git.Worktree == "" {
		return nil
	}
	git := map[string]any{}
	if s.Git.Branch != "" {
		git["branch"] = s.Git.Branch
	}
	if s.Git.Commit != "" {
		git["commit"] = s.Git.Commit
	}
	if s.Git.Worktree != "" {
		git["worktree"] = s.Git.Worktree
	}
	return git
}

// sessionMetadata is the monitor-owned dashboard projection that is not
// derivable from turn/message rows: model/provider labels, changed files,
// approval stats, and the transcript-recovered plan reference.
func sessionMetadata(s *session.Session) map[string]any {
	metadata := map[string]any{}
	if s.Model != "" {
		metadata["model"] = s.Model
	}
	if s.Provider != "" {
		metadata["provider"] = s.Provider
	}
	if len(s.Files.Read) > 0 || len(s.Files.Written) > 0 {
		metadata["files"] = s.Files
	}
	if s.Approvals.Approved > 0 || s.Approvals.Denied > 0 {
		metadata["approvals"] = s.Approvals
	}
	if s.Plan != nil {
		metadata["plan"] = s.Plan
	}
	if len(metadata) == 0 {
		return nil
	}
	return metadata
}

// subagentsDir is where Claude Code stores a session's sub-agent transcripts.
func subagentsDir(rootTranscriptPath string) string {
	return filepath.Join(strings.TrimSuffix(rootTranscriptPath, ".jsonl"), "subagents")
}
