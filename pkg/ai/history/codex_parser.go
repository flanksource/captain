package history

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
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
}

// ReadCodexSessionMeta parses only the leading metadata needed for cheap
// history candidate filtering.
func ReadCodexSessionMeta(sessionFile string) (*CodexSessionInfo, error) {
	file, err := os.Open(sessionFile)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
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
	if err := scanner.Err(); err != nil {
		return nil, err
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

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var info *CodexSessionInfo
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event, err := ParseCodexLine(line)
		if err != nil {
			continue
		}
		switch event.Type {
		case "session_meta":
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
	if err := scanner.Err(); err != nil {
		return info, err
	}
	return info, nil
}

func codexSessionInfo(event CodexEvent) CodexSessionInfo {
	info := CodexSessionInfo{
		ID:            event.Payload.ID,
		CWD:           event.Payload.CWD,
		CLIVersion:    event.Payload.CLIVersion,
		ModelProvider: event.Payload.ModelProvider,
		Originator:    event.Payload.Originator,
		StartedAt:     event.Time(),
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

func ExtractCodexToolUsesFromReader(reader io.Reader) ([]ToolUse, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	// records holds the uses of one source record per entry, in emission order.
	// Dedupe needs those boundaries to merge a message's twin records: the
	// boundary cannot be recovered afterwards, because distinct adjacent records
	// routinely share a RecordType and a millisecond.
	var records [][]ToolUse
	var sessionCWD, sessionID, currentTurn, currentModel, currentEffort string
	pendingCall := make(map[string]codexPendingCall)
	var reasoning codexReasoningCollapser
	var lineNumber int64
	stamp := func(uses []ToolUse) []ToolUse {
		for index := range uses {
			if uses[index].TurnID == "" {
				uses[index].TurnID = currentTurn
			}
			if uses[index].Model == "" {
				uses[index].Model = currentModel
			}
			if uses[index].ReasoningEffort == "" {
				uses[index].ReasoningEffort = currentEffort
			}
			if uses[index].SourceLine == 0 {
				uses[index].SourceLine = lineNumber
			}
		}
		return uses
	}
	// A record that emits nothing -- an accumulating reasoning record, an
	// unpaired function_call half, filtered internal user text -- must not count
	// as intervening content, or it would break a twin pair apart.
	add := func(uses []ToolUse) {
		if len(uses) == 0 {
			return
		}
		records = append(records, stamp(uses))
	}

	var event CodexEvent
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if err := parseCodexLineInto(&event, line); err != nil {
			log.Debugf("Error parsing codex line: %v", err)
			continue
		}
		// Every record that is not a reasoning record closes the pending span.
		// Flushing only at turn_context/EOF is what made the span grow on each
		// tailing pass -- a different count, a different dedupe key, a different
		// row -- and shifted every downstream ordinal with it.
		if !isCodexReasoningRecord(event) {
			add(reasoning.flush())
		}
		switch event.Type {
		case "session_meta":
			sessionCWD = event.Payload.CWD
			sessionID = event.Payload.ID
		case "turn_context":
			// The span was already flushed above, before the turn's model and
			// effort roll over, so the closing span is stamped with the turn it
			// belongs to. turn_context is the only reliable turn boundary: older
			// rollouts carry no turn_id at all, so keying the span on turn_id
			// alone would fold a whole multi-turn session into one bogus span.
			currentTurn = firstNonEmpty(event.Payload.TurnID, currentTurn)
			currentModel = firstNonEmpty(event.Payload.Model, currentModel)
			if IsCodexAutoReviewModel(currentModel) {
				return nil, nil
			}
			currentEffort = firstNonEmpty(event.Payload.Effort, currentEffort)
		case "response_item":
			if event.Payload.Type == "reasoning" {
				add(reasoning.observe(event, currentTurn, sessionCWD, sessionID, lineNumber))
				continue
			}
			add(extractResponseItem(event, pendingCall, sessionCWD, sessionID, lineNumber))
		case "event_msg":
			currentTurn = firstNonEmpty(event.Payload.TurnID, currentTurn)
			add(extractEventMsg(event, sessionCWD, sessionID))
		case "world_state":
			add([]ToolUse{buildCodexTopLevelEventUse(event, "world_state", sessionCWD, sessionID)})
		case "thread.started":
			sessionID = firstNonEmpty(event.ThreadID, sessionID)
		case "item.completed":
			add(extractLiveItemCompleted(event, sessionCWD, sessionID))
		case "turn.failed", "error":
			add(extractLiveError(event, sessionCWD, sessionID))
		}
	}
	// Everything flushed past this point is provisional: the transcript is still
	// being appended to, so the span can grow and the unpaired call will get its
	// output on a later pass. Ingest must keep re-offering these rows -- sealing
	// them here is what left 21% of Codex tool parts with no result forever.
	if reasoning.pending() {
		add(asProvisional(reasoning.flush()))
	}
	for _, call := range sortedCodexPendingCalls(pendingCall) {
		add(asProvisional(withSourceLine(buildPendingCallUses(call.event, sessionCWD, sessionID), call.line)))
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return dedupeCodexToolUses(records), nil
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
