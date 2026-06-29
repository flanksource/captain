package history

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
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

// CodexSessionInfo is the first-line summary of a codex rollout file:
// session id, cwd, cli version, model provider, git branch, etc.
type CodexSessionInfo struct {
	ID              string
	CWD             string
	CLIVersion      string
	ModelProvider   string
	Originator      string
	GitBranch       string
	GitCommit       string
	StartedAt       *time.Time
	Model           string // from the first turn_context payload
	ReasoningEffort string // "low", "medium", "high" — per turn_context
}

// ReadCodexSessionMeta parses only the leading session metadata needed for
// cheap history candidate filtering. It intentionally stops before scanning
// turns or response items.
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
			info := &CodexSessionInfo{
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
			return info, nil
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

// ReadCodexSessionInfo parses just the leading `session_meta` event from a
// codex rollout file. Returns nil if the file has no session_meta header.
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
			if info != nil {
				continue
			}
			info = &CodexSessionInfo{
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
			if info.Model == "" && event.Payload.Model != "" {
				info.Model = event.Payload.Model
			}
			if info.ReasoningEffort == "" && event.Payload.Effort != "" {
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

func ExtractCodexToolUsesFromReader(r io.Reader) ([]ToolUse, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var (
		toolUses     []ToolUse
		sessionCWD   string
		sessionID    string
		currentModel string
		currentEff   string
		pendingCall  = make(map[string]CodexEvent)
	)

	stamp := func(uses []ToolUse) []ToolUse {
		for i := range uses {
			if uses[i].Model == "" {
				uses[i].Model = currentModel
			}
			if uses[i].ReasoningEffort == "" {
				uses[i].ReasoningEffort = currentEff
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
			if event.Payload.Model != "" {
				currentModel = event.Payload.Model
			}
			if event.Payload.Effort != "" {
				currentEff = event.Payload.Effort
			}

		case "response_item":
			toolUses = append(toolUses, stamp(extractResponseItem(event, pendingCall, sessionCWD, sessionID))...)

		case "event_msg":
			toolUses = append(toolUses, stamp(extractEventMsg(event, sessionCWD, sessionID))...)

		// --- Newer dotted-name `codex exec --json` schema ---
		case "thread.started":
			if event.ThreadID != "" {
				sessionID = event.ThreadID
			}

		case "item.completed":
			toolUses = append(toolUses, stamp(extractLiveItemCompleted(event, sessionCWD, sessionID))...)

		case "turn.failed", "error":
			toolUses = append(toolUses, stamp(extractLiveError(event, sessionCWD, sessionID))...)

			// turn.started, turn.completed, item.started, item.delta carry
			// no tool-use information for our history view; intentionally
			// dropped to keep parity with the legacy schema.
		}
	}

	for _, callEvent := range pendingCall {
		toolUses = append(toolUses, stamp([]ToolUse{buildToolUse(callEvent, CodexEvent{}, sessionCWD, sessionID)})...)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return toolUses, nil
}

func extractResponseItem(event CodexEvent, pendingCall map[string]CodexEvent, cwd, sessionID string) []ToolUse {
	switch event.Payload.Type {
	case "function_call":
		pendingCall[event.Payload.CallID] = event
		return nil

	case "function_call_output":
		callEvent, ok := pendingCall[event.Payload.CallID]
		if !ok {
			return nil
		}
		delete(pendingCall, event.Payload.CallID)
		return []ToolUse{buildToolUse(callEvent, event, cwd, sessionID)}

	case "reasoning":
		var summaries []CodexReasoningSummary
		if len(event.Payload.Summary) > 0 {
			_ = json.Unmarshal(event.Payload.Summary, &summaries)
		}
		var text string
		for _, s := range summaries {
			if s.Text != "" {
				text = s.Text
			}
		}
		if text == "" {
			return nil
		}
		return []ToolUse{{
			Tool:      "Reasoning",
			Input:     map[string]any{"text": text},
			Timestamp: event.Time(),
			CWD:       cwd,
			SessionID: sessionID,
			Source:    "codex",
		}}

	case "message":
		if event.Payload.Role != "assistant" {
			return nil
		}
		var text string
		for _, c := range event.Payload.Content {
			if c.Type == "output_text" && c.Text != "" {
				text += c.Text
			}
		}
		if text == "" {
			return nil
		}
		return []ToolUse{{
			Tool:      "Assistant",
			Input:     map[string]any{"text": text},
			Timestamp: event.Time(),
			CWD:       cwd,
			SessionID: sessionID,
			Source:    "codex",
		}}
	}
	return nil
}

// extractLiveItemCompleted handles `item.completed` events from the newer
// `codex exec --json` schema. The item carries either a top-level Text field
// or a Content array shaped like response_item.message.
func extractLiveItemCompleted(event CodexEvent, cwd, sessionID string) []ToolUse {
	if event.Item == nil {
		return nil
	}
	text := event.Item.Text
	if text == "" {
		for _, c := range event.Item.Content {
			if c.Type == "output_text" && c.Text != "" {
				text += c.Text
			}
		}
	}
	if text == "" {
		return nil
	}
	return []ToolUse{{
		Tool:      "Assistant",
		Input:     map[string]any{"text": text},
		Timestamp: event.Time(),
		CWD:       cwd,
		SessionID: sessionID,
		Source:    "codex",
	}}
}

// extractLiveError surfaces a `turn.failed` or top-level `error` event as an
// ApiError tool use, peeling one layer of stringified-JSON nesting that
// codex sometimes uses when relaying provider errors back to the user.
func extractLiveError(event CodexEvent, cwd, sessionID string) []ToolUse {
	var msg string
	if event.Error != nil {
		msg = event.Error.Message
	}
	if msg == "" {
		msg = event.Message
	}
	msg = unwrapCodexErrorMessage(msg)
	if msg == "" {
		return nil
	}
	return []ToolUse{{
		Tool:      "ApiError",
		Input:     map[string]any{"error": msg},
		Timestamp: event.Time(),
		CWD:       cwd,
		SessionID: sessionID,
		Source:    "codex",
	}}
}

func unwrapCodexErrorMessage(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return raw
	}
	var inner CodexErrorBlock
	wrapper := struct {
		Error   *CodexErrorBlock `json:"error"`
		Message string           `json:"message"`
	}{Error: &inner}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return raw
	}
	if wrapper.Error != nil && wrapper.Error.Message != "" {
		return wrapper.Error.Message
	}
	if wrapper.Message != "" {
		return wrapper.Message
	}
	return raw
}

func extractEventMsg(event CodexEvent, cwd, sessionID string) []ToolUse {
	switch event.Payload.Type {
	case "agent_reasoning":
		if event.Payload.Text == "" {
			return nil
		}
		return []ToolUse{{
			Tool:      "Reasoning",
			Input:     map[string]any{"text": event.Payload.Text},
			Timestamp: event.Time(),
			CWD:       cwd,
			SessionID: sessionID,
			Source:    "codex",
		}}

	case "agent_message":
		if event.Payload.Message == "" {
			return nil
		}
		return []ToolUse{{
			Tool:      "Assistant",
			Input:     map[string]any{"text": event.Payload.Message},
			Timestamp: event.Time(),
			CWD:       cwd,
			SessionID: sessionID,
			Source:    "codex",
		}}
	}
	return nil
}

func buildToolUse(callEvent, outputEvent CodexEvent, cwd, sessionID string) ToolUse {
	var response string
	if outputEvent.Payload.Output != "" {
		response = extractCommandOutput(outputEvent.Payload.Output)
	}
	ts := callEvent.Time()
	if ts == nil {
		ts = outputEvent.Time()
	}

	switch callEvent.Payload.Name {
	case "update_plan":
		args := extractArgumentsMap(callEvent.Payload.Arguments)
		if plan, ok := args["plan"]; ok {
			args["todos"] = plan
		}
		return ToolUse{
			Tool:      "TodoWrite",
			Input:     args,
			Timestamp: ts,
			CWD:       cwd,
			SessionID: sessionID,
			ToolUseID: callEvent.Payload.CallID,
			Source:    "codex",
			Response:  response,
		}
	case "request_user_input":
		return ToolUse{
			Tool:      "AskUserQuestion",
			Input:     extractArgumentsMap(callEvent.Payload.Arguments),
			Timestamp: ts,
			CWD:       cwd,
			SessionID: sessionID,
			ToolUseID: callEvent.Payload.CallID,
			Source:    "codex",
			Response:  response,
		}
	}

	input := map[string]any{
		"command": extractCommand(callEvent.Payload.Arguments),
	}
	return ToolUse{
		Tool:      "Bash",
		Input:     input,
		Timestamp: ts,
		CWD:       cwd,
		SessionID: sessionID,
		ToolUseID: callEvent.Payload.CallID,
		Source:    "codex",
		Response:  response,
	}
}

func extractArgumentsMap(argsJSON string) map[string]any {
	var args map[string]any
	if argsJSON == "" || json.Unmarshal([]byte(argsJSON), &args) != nil {
		return map[string]any{}
	}
	return args
}

func extractCommand(argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var args struct {
		Cmd string `json:"cmd"`
	}
	if json.Unmarshal([]byte(argsJSON), &args) == nil && args.Cmd != "" {
		return args.Cmd
	}
	return argsJSON
}

func extractCommandOutput(raw string) string {
	if _, after, ok := strings.Cut(raw, "Output:\n"); ok {
		return after
	}
	return raw
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
		if err != nil {
			return nil
		}
		if !info.IsDir() && strings.HasSuffix(path, ".jsonl") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func IsCodexSession(path string) bool {
	return strings.Contains(path, ".codex/sessions/") || strings.Contains(path, "rollout-")
}
