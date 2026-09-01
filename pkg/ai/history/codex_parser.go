package history

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/jsonl"
	"github.com/segmentio/encoding/json"
)

func ParseCodexLine(line string) (CodexEvent, error) {
	var event CodexEvent
	err := parseCodexLineInto(&event, line)
	return event, err
}

// parseCodexLineInto resets caller-owned storage before decoding so fields
// omitted by the next record cannot leak across lines.
func parseCodexLineInto(event *CodexEvent, line string) error {
	*event = CodexEvent{}
	return json.Unmarshal([]byte(line), event)
}

func ExtractCodexToolUses(sessionFile string) ([]ToolUse, error) {
	file, err := os.Open(sessionFile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	return ExtractCodexToolUsesFromReader(file)
}

// CodexSessionInfo is the first-line summary of a codex rollout file.
type CodexSessionInfo struct {
	ID              string
	CWD             string
	CLIVersion      string
	ModelProvider   string
	Originator      string
	GitBranch       string
	GitCommit       string
	StartedAt       *time.Time
	Model           string
	ReasoningEffort string
	// ParentThreadID is set for a forked or subagent thread: the thread whose
	// history this rollout replays before its own records begin.
	ParentThreadID string
	ThreadSource   string
	AgentNickname  string
	AgentPath      string
	// HistoryStartOrdinal is the first ordinal owned by this thread; records
	// below it are the parent's and are ingested with the parent, not here.
	HistoryStartOrdinal int
}

// IsFork reports whether the rollout belongs to a thread spawned from another.
func (info CodexSessionInfo) IsFork() bool {
	return info.ParentThreadID != "" && info.ParentThreadID != info.ID
}

// ReadCodexSessionMeta parses only the leading metadata needed for cheap
// history candidate filtering.
func ReadCodexSessionMeta(sessionFile string) (*CodexSessionInfo, error) {
	file, err := os.Open(sessionFile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	for raw, err := range jsonl.Lines(file) {
		if err != nil {
			return nil, err
		}
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		event, err := ParseCodexLine(line)
		if err != nil {
			continue
		}
		switch event.Type {
		case "session_meta":
			info := codexSessionInfo(event)
			return &info, nil
		case "thread.started":
			return &CodexSessionInfo{ID: event.ThreadID, StartedAt: event.Time()}, nil
		case "turn_context", "response_item", "event_msg", "item.completed", "turn.failed", "error":
			return nil, nil
		}
	}
	return nil, nil
}

// ReadCodexSessionInfo parses the leading metadata and first turn context.
func ReadCodexSessionInfo(sessionFile string) (*CodexSessionInfo, error) {
	file, err := os.Open(sessionFile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	var info *CodexSessionInfo
	for raw, err := range jsonl.Lines(file) {
		if err != nil {
			return info, err
		}
		line := strings.TrimSpace(string(raw))
		if line == "" {
			continue
		}
		event, err := ParseCodexLine(line)
		if err != nil {
			continue
		}
		switch event.Type {
		case "session_meta":
			// A fork replays its parent's session_meta after its own; only the
			// first record describes this rollout.
			if info == nil {
				value := codexSessionInfo(event)
				info = &value
			}
		case "thread.started":
			if info == nil {
				info = &CodexSessionInfo{StartedAt: event.Time()}
			}
			if info.ID == "" {
				info.ID = event.ThreadID
			}
		case "turn_context":
			if info == nil {
				info = &CodexSessionInfo{StartedAt: event.Time()}
			}
			if info.Model == "" {
				info.Model = event.Payload.Model
			}
			if info.ReasoningEffort == "" {
				info.ReasoningEffort = event.Payload.Effort
			}
			if info.Model != "" && info.ReasoningEffort != "" {
				return info, nil
			}
		}
	}
	return info, nil
}

func codexSessionInfo(event CodexEvent) CodexSessionInfo {
	info := CodexSessionInfo{
		ID:             event.Payload.ID,
		CWD:            event.Payload.CWD,
		CLIVersion:     event.Payload.CLIVersion,
		ModelProvider:  event.Payload.ModelProvider,
		Originator:     event.Payload.Originator,
		StartedAt:      event.Time(),
		ParentThreadID: firstNonEmpty(event.Payload.ParentThreadID, event.Payload.ForkedFromID),
		ThreadSource:   event.Payload.ThreadSource,
		AgentNickname:  event.Payload.AgentNickname,
		AgentPath:      event.Payload.AgentPath,
	}
	if start := event.Payload.SubagentHistoryStartOrdinal; start != nil {
		info.HistoryStartOrdinal = *start
	}
	if event.Payload.Git != nil {
		info.GitBranch = event.Payload.Git.Branch
		info.GitCommit = event.Payload.Git.CommitHash
	}
	return info
}

func IsCodexAutoReviewSession(sessionFile string) (bool, error) {
	info, err := ReadCodexSessionInfo(sessionFile)
	if err != nil || info == nil {
		return false, err
	}
	return IsCodexAutoReviewModel(info.Model), nil
}

func IsCodexAutoReviewModel(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), api.CodexAutoReviewModel)
}

// CodexParser retains the minimum rollout state needed to parse records added
// after a known file offset. Call Snapshot after each append to project rows
// that are valid at the current EOF but may still change on the next append.
type CodexParser struct {
	sessionCWD    string
	sessionID     string
	currentTurn   string
	currentModel  string
	currentEffort string
	pendingCall   map[string]codexPendingCall
	reasoning     codexReasoningCollapser
	deduper       codexDeduper
	info          *CodexSessionInfo
	event         CodexEvent
	lineNumber    int64
	ignored       bool
}

func NewCodexParser() *CodexParser {
	return &CodexParser{pendingCall: make(map[string]codexPendingCall)}
}

// ConsumeLine advances the parser by one physical JSONL line and returns only
// rows whose content can no longer change as this rollout grows. Malformed
// records retain the historical best-effort behavior: they advance line
// identity but do not poison the rest of the transcript.
func (p *CodexParser) ConsumeLine(line string) []ToolUse {
	p.lineNumber++
	line = strings.TrimSpace(line)
	if line == "" || p.ignored {
		return nil
	}
	if err := parseCodexLineInto(&p.event, line); err != nil {
		log.Debugf("Error parsing codex line: %v", err)
		return nil
	}
	if p.info != nil && p.info.IsFork() && p.event.Ordinal != nil &&
		*p.event.Ordinal < p.info.HistoryStartOrdinal {
		return nil
	}

	var settled []ToolUse
	add := func(uses []ToolUse) {
		if len(uses) == 0 {
			return
		}
		settled = append(settled, p.deduper.push(p.stamp(uses))...)
	}

	// Every non-reasoning record closes the pending span before its own state is
	// applied, so the span retains the model and turn that produced it.
	if !isCodexReasoningRecord(p.event) {
		add(p.reasoning.flush())
	}
	switch p.event.Type {
	case "session_meta":
		p.sessionCWD = p.event.Payload.CWD
		p.sessionID = p.event.Payload.ID
		if p.info == nil {
			info := codexSessionInfo(p.event)
			p.info = &info
		}
	case "turn_context":
		p.currentTurn = firstNonEmpty(p.event.Payload.TurnID, p.currentTurn)
		p.currentModel = firstNonEmpty(p.event.Payload.Model, p.currentModel)
		p.currentEffort = firstNonEmpty(p.event.Payload.Effort, p.currentEffort)
		p.observeTurnInfo()
		if IsCodexAutoReviewModel(p.currentModel) {
			p.ignored = true
			return nil
		}
	case "response_item":
		if p.event.Payload.Type == "reasoning" {
			add(p.reasoning.observe(p.event, p.currentTurn, p.sessionCWD, p.sessionID, p.lineNumber))
			return settled
		}
		add(extractResponseItem(p.event, p.pendingCall, p.sessionCWD, p.sessionID, p.lineNumber))
	case "event_msg":
		p.currentTurn = firstNonEmpty(p.event.Payload.TurnID, p.currentTurn)
		add(extractEventMsg(p.event, p.sessionCWD, p.sessionID))
	case "world_state":
		add([]ToolUse{buildCodexTopLevelEventUse(p.event, "world_state", p.sessionCWD, p.sessionID)})
	case "thread.started":
		p.sessionID = firstNonEmpty(p.event.ThreadID, p.sessionID)
		p.observeThreadInfo()
	case "item.completed":
		add(extractLiveItemCompleted(p.event, p.sessionCWD, p.sessionID))
	case "turn.failed", "error":
		add(extractLiveError(p.event, p.sessionCWD, p.sessionID))
	}
	return settled
}

func (p *CodexParser) stamp(uses []ToolUse) []ToolUse {
	for index := range uses {
		if uses[index].TurnID == "" {
			uses[index].TurnID = p.currentTurn
		}
		if uses[index].Model == "" {
			uses[index].Model = p.currentModel
		}
		if uses[index].ReasoningEffort == "" {
			uses[index].ReasoningEffort = p.currentEffort
		}
		if uses[index].SourceLine == 0 {
			uses[index].SourceLine = p.lineNumber
		}
	}
	return uses
}

func (p *CodexParser) observeThreadInfo() {
	if p.info == nil {
		p.info = &CodexSessionInfo{StartedAt: p.event.Time()}
	}
	if p.info.ID == "" {
		p.info.ID = p.event.ThreadID
	}
}

func (p *CodexParser) observeTurnInfo() {
	if p.info == nil {
		p.info = &CodexSessionInfo{StartedAt: p.event.Time()}
	}
	if p.info.Model == "" {
		p.info.Model = p.event.Payload.Model
	}
	if p.info.ReasoningEffort == "" {
		p.info.ReasoningEffort = p.event.Payload.Effort
	}
}

// Snapshot returns the unresolved tail without advancing parser state. Dedupe
// candidates, open reasoning spans, and unpaired calls are re-created on every
// append until a real source record settles them.
func (p *CodexParser) Snapshot() []ToolUse {
	uses := p.deduper.snapshot()
	if p.reasoning.pending() {
		reasoning := p.reasoning
		uses = append(uses, p.stamp(asProvisional(reasoning.flush()))...)
	}
	for _, call := range sortedCodexPendingCalls(p.pendingCall) {
		uses = append(uses,
			p.stamp(asProvisional(withSourceLine(buildPendingCallUses(call.event, p.sessionCWD, p.sessionID), call.line)))...)
	}
	return uses
}

// SessionInfo returns the metadata observed by this parser so incremental
// callers do not need a second scan of the transcript header.
func (p *CodexParser) SessionInfo() *CodexSessionInfo {
	if p.info == nil {
		return nil
	}
	info := *p.info
	return &info
}

func (p *CodexParser) LineNumber() int64 { return p.lineNumber }
func (p *CodexParser) Ignored() bool     { return p.ignored }

func ExtractCodexToolUsesFromReader(reader io.Reader) ([]ToolUse, error) {
	parser := NewCodexParser()
	var uses []ToolUse
	for line, err := range jsonl.Lines(reader) {
		if err != nil {
			return nil, err
		}
		uses = append(uses, parser.ConsumeLine(string(line))...)
	}
	if parser.Ignored() {
		return nil, nil
	}
	return append(uses, parser.Snapshot()...), nil
}

// buildPendingCallUses builds the provisional row for a call whose output never
// arrived, dispatching on the call's own record type the way the paired path
// dispatches on the output's. Routing every leftover through buildToolUses gave a
// tool_search_call the wrong shape entirely.
func buildPendingCallUses(callEvent CodexEvent, cwd, sessionID string) []ToolUse {
	if callEvent.Payload.Type == "tool_search_call" {
		return buildPendingToolSearchUse(callEvent, cwd, sessionID)
	}
	return buildToolUses(callEvent, CodexEvent{}, cwd, sessionID)
}

func isCodexReasoningRecord(event CodexEvent) bool {
	return event.Type == "response_item" && event.Payload.Type == "reasoning"
}

// sortedCodexPendingCalls orders the leftover calls by the line they arrived on.
// Map iteration order would otherwise make the tail of the transcript differ
// between two parses of the same bytes.
func sortedCodexPendingCalls(pending map[string]codexPendingCall) []codexPendingCall {
	calls := make([]codexPendingCall, 0, len(pending))
	for _, call := range pending {
		calls = append(calls, call)
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].line < calls[j].line })
	return calls
}

func FindCodexSessionFiles() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	sessionsDir := filepath.Join(home, ".codex", "sessions")
	if _, err := os.Stat(sessionsDir); os.IsNotExist(err) {
		return nil, nil
	}
	var files []string
	err = filepath.Walk(sessionsDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func IsCodexSession(path string) bool {
	return strings.Contains(path, ".codex/sessions/") || strings.Contains(path, "rollout-")
}
