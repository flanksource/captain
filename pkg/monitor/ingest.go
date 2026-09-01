package monitor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/database"
	"github.com/flanksource/captain/pkg/session"
)

// parserVersion invalidates every ingested transcript when the parsing or
// mapping logic changes shape.
const parserVersion = 5

type codexCheckpoint struct {
	parser      *history.CodexParser
	accumulator *session.CodexAccumulator
	offset      int64
	observed    os.FileInfo
	identity    string
	hasUses     bool
}

type codexPreparation struct {
	checkpoint *codexCheckpoint
	session    *session.Session
	offset     int64
	resetMark  bool
}

// ingestor turns changed transcript files into native database rows, skipping
// files whose recorded mtime/size/parser version still match the disk state.
type ingestor struct {
	monitor *Monitor
	db      *database.DB
	// watchSubagents arms the watcher on a root transcript's subagents
	// directory; nil outside watched (one-shot) runs.
	watchSubagents func(rootTranscriptPath string)
	// requeue schedules another pass when a descriptor-bounded parse observes
	// that the path changed again before its database write completed.
	requeue func(ctx context.Context, source, path string)

	mu          sync.Mutex
	sources     map[string]database.SessionSourceState
	codex       map[string]*codexCheckpoint
	ingestLocks sync.Map // transcript path -> *sync.Mutex
}

func newIngestor(m *Monitor) *ingestor {
	return &ingestor{
		monitor: m, db: m.db,
		sources: map[string]database.SessionSourceState{}, codex: map[string]*codexCheckpoint{},
	}
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

func (ing *ingestor) prepareCodexCheckpoint(path string, info os.FileInfo) (*codexCheckpoint, bool) {
	ing.mu.Lock()
	defer ing.mu.Unlock()
	checkpoint := ing.codex[path]
	if checkpoint == nil {
		return &codexCheckpoint{
			parser: history.NewCodexParser(), accumulator: session.NewCodexAccumulator(path),
		}, false
	}
	state, ok := ing.sources[path]
	valid := checkpoint.parser != nil && checkpoint.accumulator != nil && checkpoint.observed != nil &&
		ok && state.ParserVersion == parserVersion &&
		state.ByteOffset == checkpoint.offset && state.ObservedSize == checkpoint.observed.Size() &&
		state.SourceIdentity == checkpoint.identity && checkpoint.offset >= 0 && checkpoint.offset <= info.Size() &&
		os.SameFile(checkpoint.observed, info) && info.Size() >= checkpoint.observed.Size()
	if valid && info.Size() == checkpoint.observed.Size() {
		valid = observedModTime(info).Equal(observedModTime(checkpoint.observed))
	}
	if valid {
		return checkpoint, false
	}
	delete(ing.codex, path)
	return &codexCheckpoint{
		parser: history.NewCodexParser(), accumulator: session.NewCodexAccumulator(path),
	}, true
}

func (ing *ingestor) commitCodexCheckpoint(path string, preparation *codexPreparation, info os.FileInfo, identity string) {
	checkpoint := preparation.checkpoint
	checkpoint.offset = preparation.offset
	checkpoint.observed = info
	checkpoint.identity = identity
	ing.mu.Lock()
	ing.codex[path] = checkpoint
	ing.mu.Unlock()
}

func (ing *ingestor) dropCodexCheckpoint(path string) {
	ing.mu.Lock()
	delete(ing.codex, path)
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
	if info.Size() == state.ObservedSize && state.ObservedModTime != nil &&
		!state.ObservedModTime.Equal(observedModTime(info)) {
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

	var codexFile *os.File
	var info os.FileInfo
	if source == "codex" {
		codexFile, err = rootFS.Open(relativePath)
		if err == nil {
			defer codexFile.Close()
			info, err = codexFile.Stat()
		}
	} else {
		info, err = rootFS.Stat(relativePath)
	}
	if err != nil {
		if os.IsNotExist(err) {
			ing.dropCodexCheckpoint(path)
			ing.monitor.untrackTranscript(path)
			return nil
		}
		return err
	}
	if !ing.needsIngest(path, info) {
		return nil
	}
	var input database.IngestTranscriptInput
	var codex *codexPreparation
	switch source {
	case "claude":
		input, err = ing.claudeIngestInput(ctx, path)
	case "codex":
		var ignored bool
		input, codex, ignored, err = ing.incrementalCodexIngestInput(codexFile, path, info)
		if ignored {
			ing.dropCodexCheckpoint(path)
			return nil
		}
	default:
		return fmt.Errorf("unknown transcript source %q for %s", source, path)
	}
	if err != nil {
		if source == "codex" {
			ing.dropCodexCheckpoint(path)
		}
		return err
	}
	if codex != nil && codex.session.ForkedFrom != "" {
		parent, err := ing.db.CreateOrGetSession(ctx, database.CreateSessionInput{
			ProviderSessionID: codex.session.ForkedFrom, Source: "codex", HostID: ing.monitor.cfg.HostID,
		})
		if err != nil {
			ing.dropCodexCheckpoint(path)
			return err
		}
		input.Session.ParentSessionID = &parent.ID
		if codex.session.Root != nil {
			input.Session.AgentType = codex.session.Root.Type
			input.Session.Description = codex.session.Root.Desc
		}
	}
	input.Session.HostID = ing.monitor.cfg.HostID
	input.Source.SourceKind = source
	input.Source.Path = path
	input.Source.ParserVersion = parserVersion
	input.Source.ObservedSize = info.Size()
	input.Source.ByteOffset = info.Size()
	if codex != nil {
		input.Source.ByteOffset = codex.offset
	}
	input.Source.ObservedModTime = observedModTime(info)

	metrics := &ing.monitor.ingest
	metrics.messagesParsed.Add(int64(len(input.Messages)))
	previous := ing.resumeAfter(path, info)
	if codex != nil && codex.resetMark {
		previous = 0
	}
	if state, ok := ing.sourceState(path); ok && state.SourceIdentity != "" &&
		state.SourceIdentity != input.Source.SourceIdentity {
		previous = 0
	}
	input.Source.LastEventKey = strconv.FormatInt(highWaterMark(&input, previous), 10)
	metrics.messagesOffered.Add(int64(len(input.Messages)))
	metrics.filesIngested.Add(1)

	writeStart := time.Now()
	persisted, err := ing.db.IngestTranscript(ctx, input)
	metrics.writeNanos.Add(int64(time.Since(writeStart)))
	if err != nil {
		if codex != nil {
			ing.dropCodexCheckpoint(path)
		}
		return err
	}
	if source == "claude" && input.Session.ParentSessionID == nil && ing.watchSubagents != nil {
		ing.watchSubagents(path)
	}
	modTime := input.Source.ObservedModTime
	ing.recordSourceState(database.SessionSourceState{
		SessionID: persisted.ID, SourceKind: source, Path: path, SourceIdentity: input.Source.SourceIdentity,
		ParserVersion: parserVersion,
		ByteOffset:    input.Source.ByteOffset, ObservedSize: input.Source.ObservedSize, ObservedModTime: &modTime,
		LastEventKey: input.Source.LastEventKey,
	})
	if codex != nil {
		ing.commitCodexCheckpoint(path, codex, info, input.Source.SourceIdentity)
		if ing.requeue != nil {
			latest, statErr := rootFS.Stat(relativePath)
			if statErr == nil && (!os.SameFile(info, latest) || latest.Size() != info.Size() ||
				!observedModTime(latest).Equal(observedModTime(info))) {
				ing.requeue(ctx, source, path)
			}
		}
	}
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

func (ing *ingestor) incrementalCodexIngestInput(file *os.File, path string, info os.FileInfo) (
	database.IngestTranscriptInput, *codexPreparation, bool, error,
) {
	checkpoint, resetMark := ing.prepareCodexCheckpoint(path, info)
	settled, offset, err := consumeCodexSuffix(file, checkpoint.offset, info.Size(), checkpoint.parser)
	if err != nil {
		return database.IngestTranscriptInput{}, nil, false, err
	}
	preparation := &codexPreparation{checkpoint: checkpoint, offset: offset, resetMark: resetMark}
	if checkpoint.parser.Ignored() {
		return database.IngestTranscriptInput{}, preparation, true, nil
	}
	provisional := checkpoint.parser.Snapshot()
	if len(settled) > 0 || len(provisional) > 0 {
		checkpoint.hasUses = true
	}
	if !checkpoint.hasUses {
		return database.IngestTranscriptInput{}, preparation, false,
			fmt.Errorf("codex transcript %s is not parseable", path)
	}
	checkpoint.accumulator.Add(checkpoint.parser.SessionInfo(), settled)
	s := checkpoint.accumulator.Project(provisional)
	if strings.TrimSpace(s.ID) == "" {
		return database.IngestTranscriptInput{}, preparation, false,
			fmt.Errorf("codex transcript %s has no session identity", path)
	}
	input := unifiedIngestInput(s, "codex", transcriptSequence)
	input.Session.ProviderSessionID = s.ID
	input.Source.SourceIdentity = s.ID
	preparation.session = s
	return input, preparation, false, nil
}

// consumeCodexSuffix parses only complete newline-terminated records inside the
// descriptor snapshot [offset, size]. A partial final record leaves the cursor
// before it so the next append re-reads the complete JSON value.
func consumeCodexSuffix(file *os.File, offset, size int64, parser *history.CodexParser) ([]history.ToolUse, int64, error) {
	if file == nil || parser == nil || offset < 0 || offset > size {
		return nil, offset, fmt.Errorf("invalid Codex parser checkpoint %d for file size %d", offset, size)
	}
	reader := bufio.NewReader(io.NewSectionReader(file, offset, size-offset))
	position := offset
	var uses []history.ToolUse
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, position, err
		}
		position += int64(len(line))
		uses = append(uses, parser.ConsumeLine(line)...)
	}
	return uses, position, nil
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

// transcriptProvider maps a transcript's writing agent onto the provider whose
// models it billed. It answers the provider axis only — see unifiedIngestInput.
func transcriptProvider(source string) string {
	for _, p := range api.Providers() {
		if p.AgentName == source {
			return p.Name
		}
	}
	return ""
}

// unifiedIngestInput maps the unified session model onto the native ingest
// batch: turns become turn rows with one aggregate model call each, and
// conversational messages become message rows keyed by sequence.
// unifiedIngestInput builds the ingest payload from one transcript. source is
// the agent that WROTE the transcript ("claude"/"codex"), which names the
// provider but never the mode: a transcript looks identical whether the agent ran
// under the cli, the agent SDK, or cmux. Model calls ingested here therefore
// record a provider and leave mode unset, rather than inventing one.
func unifiedIngestInput(s *session.Session, source string, sequence func(session.Message, int) int64) database.IngestTranscriptInput {
	input := database.IngestTranscriptInput{
		Session: database.IngestSessionInput{
			Source: source, Path: s.HistoryFile, Project: s.Project, CWD: s.CWD,
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
				Model: turn.Model, Provider: transcriptProvider(source), Effort: turn.ReasoningEffort,
				InputTokens:  int64(turn.Usage.InputTokens),
				OutputTokens: int64(turn.Usage.OutputTokens), ReasoningTokens: int64(turn.Usage.ReasoningTokens),
				CacheReadTokens: int64(turn.Usage.CacheReadTokens), CacheWriteTokens: int64(turn.Usage.CacheWriteTokens),
				InputCost: turn.Cost.InputCost, OutputCost: turn.Cost.OutputCost, ReasoningCost: turn.Cost.ReasoningCost,
				CacheReadCost: turn.Cost.CacheReadCost, CacheWriteCost: turn.Cost.CacheWriteCost,
				Currency:  "USD",
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
