package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
)

// parserVersion invalidates every ingested transcript when the parsing or
// mapping logic changes shape.
const parserVersion = 4

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

// resumeAfter returns the highest message sequence a previous pass already
// persisted for a transcript. Transcripts are append-only, so one appended line
// would otherwise re-submit every message in the file — millions of insert
// round-trips to write a handful of rows.
//
// The mark is void whenever the stored rows cannot be trusted to match what the
// current parser would produce: a parser-version bump changes the mapping, and
// a file that shrank was rewritten rather than appended to. Both fall back to a
// full replay, which is idempotent.
func (ing *ingestor) resumeAfter(path string, info os.FileInfo) int64 {
	state, ok := ing.sourceState(path)
	if !ok || state.ParserVersion != parserVersion || info.Size() < state.ObservedSize {
		return 0
	}
	mark, err := strconv.ParseInt(state.LastEventKey, 10, 64)
	if err != nil || mark < 0 {
		return 0
	}
	return mark
}

// highWaterMark drops the messages a previous pass already wrote and returns the
// mark to record for the next one. Turns are deliberately left whole: they carry
// running aggregates over the entire file, so the newest turn's totals change on
// every append and there are few enough of them to re-upsert in one batch.
//
// The mark never advances past a provisional row. A tool call parsed before its
// result was written, or a reasoning span still open at EOF, is correct only
// until the next append; sealing it behind the mark meant the completed row was
// dropped on the following pass and the truncated one stood forever.
func highWaterMark(input *database.IngestTranscriptInput, previous int64) int64 {
	ceiling := int64(-1)
	for _, message := range input.Messages {
		if message.Provisional && (ceiling < 0 || message.Sequence < ceiling) {
			ceiling = message.Sequence
		}
	}

	mark := previous
	fresh := input.Messages[:0]
	for _, message := range input.Messages {
		if message.Sequence > mark && (ceiling < 0 || message.Sequence < ceiling) {
			mark = message.Sequence
		}
		if message.Sequence > previous {
			fresh = append(fresh, message)
		}
	}
	input.Messages = fresh
	return mark
}

// observedModTime is a file's mtime at the precision the bookkeeping column can
// actually hold. APFS reports nanoseconds, timestamptz stores microseconds, so
// comparing a raw stat against a value that has been through the database is a
// comparison that can never succeed: needsIngest returned true for every file
// on every pass, and the whole skip mechanism was inert.
func observedModTime(info os.FileInfo) time.Time {
	return info.ModTime().UTC().Truncate(time.Microsecond)
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
		!state.ObservedModTime.Equal(observedModTime(info))
}

// ingestFile parses one transcript and persists it. Claude sub-agent files are
// child sessions of the root transcript's session; root ingests also arm the
// watcher on the session's subagents directory.
func (ing *ingestor) ingestFile(ctx context.Context, source, path string) error {
	root, err := hookTranscriptRoot(source)
	if err != nil {
		return err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve %s transcript root: %w", source, err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %s transcript path: %w", source, err)
	}
	if !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return fmt.Errorf("transcript path %s is outside the %s session root", path, source)
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open %s transcript root: %w", source, err)
	}
	defer rootFS.Close()
	relativePath, err := filepath.Rel(root, path)
	if err != nil {
		return fmt.Errorf("resolve %s transcript relative path: %w", source, err)
	}

	lockValue, _ := ing.ingestLocks.LoadOrStore(path, &sync.Mutex{})
	pathLock := lockValue.(*sync.Mutex)
	pathLock.Lock()
	defer pathLock.Unlock()

	info, err := rootFS.Stat(relativePath)
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
	input.Source.ObservedModTime = observedModTime(info)

	// Parsed against offered is the standing regression test for the high-water
	// mark: the parse is always whole-file, the write should not be.
	metrics := &ing.monitor.ingest
	metrics.messagesParsed.Add(int64(len(input.Messages)))
	input.Source.LastEventKey = strconv.FormatInt(highWaterMark(&input, ing.resumeAfter(path, info)), 10)
	metrics.messagesOffered.Add(int64(len(input.Messages)))
	metrics.filesIngested.Add(1)

	writeStart := time.Now()
	persisted, err := ing.db.IngestTranscript(ctx, input)
	metrics.writeNanos.Add(int64(time.Since(writeStart)))
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
		LastEventKey: input.Source.LastEventKey,
	})
	return nil
}

func (ing *ingestor) claudeIngestInput(ctx context.Context, path string) (database.IngestTranscriptInput, error) {
	s, info, err := session.BuildTranscriptFile(path)
	if err != nil {
		return database.IngestTranscriptInput{}, err
	}
	input := unifiedIngestInput(s, "claude", transcriptSequence)
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
	input := unifiedIngestInput(s, "codex", transcriptSequence)
	input.Session.ProviderSessionID = s.ID
	input.Source.SourceIdentity = s.ID
	return input, nil
}

// transcriptSequence keys messages by their transcript line: stable across
// re-parses and the seek reference the UI uses against the raw file. Codex used
// to key on the message's ordinal in the freshly parsed slice instead, so a
// single collapsed reasoning span shifting by one renumbered the rest of the
// file and the high-water mark discarded every renumbered row.
func transcriptSequence(m session.Message, _ int) int64 { return m.SourceLine }

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
			Provisional: m.Provisional,
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
	return session.Metadata{
		Model: s.Model, Provider: s.Provider, Files: s.Files,
		Todos: s.Todos, Approvals: s.Approvals, Plan: s.Plan,
	}.Encode()
}

// subagentsDir is where Claude Code stores a session's sub-agent transcripts.
func subagentsDir(rootTranscriptPath string) string {
	return filepath.Join(strings.TrimSuffix(rootTranscriptPath, ".jsonl"), "subagents")
}
