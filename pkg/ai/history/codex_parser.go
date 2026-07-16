package history

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/api"
	"github.com/segmentio/encoding/json"
)

func ParseCodexLine(line string) (CodexEvent, error) {
	var event CodexEvent
	err := json.Unmarshal([]byte(line), &event)
	return event, err
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

	var toolUses []ToolUse
	var sessionCWD, sessionID, currentTurn, currentModel, currentEffort string
	pendingCall := make(map[string]CodexEvent)
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
		}
		return uses
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		event, err := ParseCodexLine(line)
		if err != nil {
			log.Debugf("Error parsing codex line: %v", err)
			continue
		}
		switch event.Type {
		case "session_meta":
			sessionCWD = event.Payload.CWD
			sessionID = event.Payload.ID
		case "turn_context":
			currentTurn = firstNonEmpty(event.Payload.TurnID, currentTurn)
			currentModel = firstNonEmpty(event.Payload.Model, currentModel)
			if IsCodexAutoReviewModel(currentModel) {
				return nil, nil
			}
			currentEffort = firstNonEmpty(event.Payload.Effort, currentEffort)
		case "response_item":
			toolUses = append(toolUses, stamp(extractResponseItem(event, pendingCall, sessionCWD, sessionID))...)
		case "event_msg":
			currentTurn = firstNonEmpty(event.Payload.TurnID, currentTurn)
			toolUses = append(toolUses, stamp(extractEventMsg(event, sessionCWD, sessionID))...)
		case "world_state":
			toolUses = append(toolUses, stamp([]ToolUse{buildCodexTopLevelEventUse(event, "world_state", sessionCWD, sessionID)})...)
		case "thread.started":
			sessionID = firstNonEmpty(event.ThreadID, sessionID)
		case "item.completed":
			toolUses = append(toolUses, stamp(extractLiveItemCompleted(event, sessionCWD, sessionID))...)
		case "turn.failed", "error":
			toolUses = append(toolUses, stamp(extractLiveError(event, sessionCWD, sessionID))...)
		}
	}
	for _, callEvent := range pendingCall {
		toolUses = append(toolUses, stamp([]ToolUse{buildToolUse(callEvent, CodexEvent{}, sessionCWD, sessionID)})...)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return dedupeCodexToolUses(toolUses), nil
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
