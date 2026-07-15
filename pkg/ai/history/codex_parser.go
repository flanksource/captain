package history

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai/assistanttags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude/tools"
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

// IsCodexAutoReviewSession reports whether a rollout belongs to Codex's
// internal approval reviewer. Match parsed model metadata only: ordinary user
// prompts and transcripts may legitimately mention the model name.
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

func ExtractCodexToolUsesFromReader(r io.Reader) ([]ToolUse, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var (
		toolUses     []ToolUse
		sessionCWD   string
		sessionID    string
		currentTurn  string
		currentModel string
		currentEff   string
		pendingCall  = make(map[string]CodexEvent)
	)

	stamp := func(uses []ToolUse) []ToolUse {
		for i := range uses {
			if uses[i].TurnID == "" {
				uses[i].TurnID = currentTurn
			}
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
			if event.Payload.TurnID != "" {
				currentTurn = event.Payload.TurnID
			}
			if event.Payload.Model != "" {
				currentModel = event.Payload.Model
				if IsCodexAutoReviewModel(currentModel) {
					return nil, nil
				}
			}
			if event.Payload.Effort != "" {
				currentEff = event.Payload.Effort
			}

		case "response_item":
			toolUses = append(toolUses, stamp(extractResponseItem(event, pendingCall, sessionCWD, sessionID))...)

		case "event_msg":
			if event.Payload.TurnID != "" {
				currentTurn = event.Payload.TurnID
			}
			toolUses = append(toolUses, stamp(extractEventMsg(event, sessionCWD, sessionID))...)

		case "world_state":
			toolUses = append(toolUses, stamp([]ToolUse{buildCodexTopLevelEventUse(event, "world_state", sessionCWD, sessionID)})...)

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
	return dedupeCodexToolUses(toolUses), nil
}

func extractResponseItem(event CodexEvent, pendingCall map[string]CodexEvent, cwd, sessionID string) []ToolUse {
	switch event.Payload.Type {
	case "function_call":
		pendingCall[event.Payload.CallID] = event
		return nil

	case "tool_search_call":
		pendingCall[event.Payload.CallID] = event
		return nil

	case "function_call_output":
		callEvent, ok := pendingCall[event.Payload.CallID]
		if !ok {
			return nil
		}
		delete(pendingCall, event.Payload.CallID)
		return []ToolUse{buildToolUse(callEvent, event, cwd, sessionID)}

	case "tool_search_output":
		callEvent, ok := pendingCall[event.Payload.CallID]
		if ok {
			delete(pendingCall, event.Payload.CallID)
		}
		return buildToolSearchUses(callEvent, event, cwd, sessionID)

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
			Tool:       "Reasoning",
			Input:      map[string]any{"text": text},
			Timestamp:  event.Time(),
			CWD:        cwd,
			SessionID:  sessionID,
			TurnID:     codexEventTurnID(event),
			Source:     "codex",
			RecordType: "response_item.reasoning",
		}}

	case "message":
		var tool string
		var text string
		switch event.Payload.Role {
		case "assistant":
			text = codexContentText(event.Payload.Content, "output_text", "text")
			return extractCodexAssistantText(text, event, cwd, sessionID, "response_item.message")
		case "user":
			text = codexContentText(event.Payload.Content, "input_text", "text")
			if shell, ok := parseCodexUserShellCommand(text); ok {
				return []ToolUse{buildCodexUserShellCommandUse(shell, event, cwd, sessionID, "response_item.message.user_shell_command")}
			}
			var ok bool
			if tool, ok = codexUserMessageTool(text); !ok {
				return nil
			}
		default:
			return nil
		}
		if text == "" {
			return nil
		}
		return []ToolUse{{
			Tool:       tool,
			Input:      map[string]any{"text": text},
			Timestamp:  event.Time(),
			CWD:        cwd,
			SessionID:  sessionID,
			TurnID:     codexEventTurnID(event),
			Source:     "codex",
			RecordType: "response_item.message",
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
	if event.Item.Role != "" && event.Item.Role != "assistant" {
		return nil
	}
	text := event.Item.Text
	if text == "" {
		text = codexContentText(event.Item.Content, "output_text", "text")
	}
	if text == "" {
		return nil
	}
	return extractCodexAssistantText(text, event, cwd, sessionID, "item.completed")
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
		TurnID:    codexEventTurnID(event),
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
			Tool:       "Reasoning",
			Input:      map[string]any{"text": event.Payload.Text},
			Timestamp:  event.Time(),
			CWD:        cwd,
			SessionID:  sessionID,
			TurnID:     codexEventTurnID(event),
			Source:     "codex",
			RecordType: "event_msg.agent_reasoning",
		}}

	case "agent_message":
		if event.Payload.Message == "" {
			return nil
		}
		return extractCodexAssistantText(event.Payload.Message, event, cwd, sessionID, "event_msg.agent_message")

	case "user_message":
		text := event.Payload.Message
		if text == "" {
			text = event.Payload.Text
		}
		if text == "" {
			return nil
		}
		if shell, ok := parseCodexUserShellCommand(text); ok {
			return []ToolUse{buildCodexUserShellCommandUse(shell, event, cwd, sessionID, "event_msg.user_shell_command")}
		}
		tool, ok := codexUserMessageTool(text)
		if !ok {
			return nil
		}
		return []ToolUse{{
			Tool:       tool,
			Input:      map[string]any{"text": text},
			Timestamp:  event.Time(),
			CWD:        cwd,
			SessionID:  sessionID,
			TurnID:     codexEventTurnID(event),
			Source:     "codex",
			RecordType: "event_msg.user_message",
		}}
	}
	if event.Payload.Type == "" {
		return nil
	}
	return []ToolUse{buildCodexEventUse(event, cwd, sessionID)}
}

func extractCodexAssistantText(text string, event CodexEvent, cwd, sessionID, recordType string) []ToolUse {
	segments := assistanttags.Parse(text)
	uses := make([]ToolUse, 0, len(segments))
	for _, segment := range segments {
		use := ToolUse{
			Timestamp:  event.Time(),
			CWD:        cwd,
			SessionID:  sessionID,
			TurnID:     codexEventTurnID(event),
			Source:     "codex",
			RecordType: recordType,
		}
		switch segment.Kind {
		case assistanttags.SegmentPlan:
			use.Tool = "Plan"
			use.Input = map[string]any{"content": segment.Text, "tag": "proposed_plan"}
		case assistanttags.SegmentMemoryCitation:
			if segment.Citation == nil {
				continue
			}
			use.Tool = "MemoryCitation"
			use.Input = map[string]any{
				"event":            "memory_citation",
				"source":           "codex",
				"citation_entries": segment.Citation.CitationEntries,
				"rollout_ids":      segment.Citation.RolloutIDs,
			}
		case assistanttags.SegmentText:
			use.Tool = "Assistant"
			body := segment.Text
			if summary, ok := assistanttags.EnvelopeSummary(body); ok {
				body = summary
			}
			use.Input = map[string]any{"text": body}
		default:
			continue
		}
		uses = append(uses, use)
	}
	return uses
}

func buildToolSearchUses(callEvent, outputEvent CodexEvent, cwd, sessionID string) []ToolUse {
	names := codexToolSearchNames(outputEvent.Payload.Tools)
	if len(names) == 0 {
		return nil
	}
	input := map[string]any{
		"event":      "deferred_tools_delta",
		"addedNames": names,
	}
	if len(callEvent.Payload.Arguments) > 0 {
		for k, v := range extractArgumentsMap(callEvent.Payload.Arguments) {
			input[k] = v
		}
	}
	return []ToolUse{{
		Tool:       "DeferredToolsDelta",
		Input:      input,
		Timestamp:  outputEvent.Time(),
		CWD:        cwd,
		SessionID:  sessionID,
		TurnID:     firstNonEmpty(codexEventTurnID(outputEvent), codexEventTurnID(callEvent)),
		ToolUseID:  outputEvent.Payload.CallID,
		Source:     "codex",
		RecordType: "response_item.tool_search_output",
	}}
}

func codexToolSearchNames(groups []CodexToolSearchNamespace) []string {
	var names []string
	seen := map[string]struct{}{}
	add := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" {
			return
		}
		if _, ok := seen[name]; ok {
			return
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	for _, group := range groups {
		for _, tool := range group.Tools {
			add(tool.Name)
		}
	}
	return names
}

func codexContentText(content []CodexContent, accepted ...string) string {
	accept := make(map[string]struct{}, len(accepted))
	for _, typ := range accepted {
		accept[typ] = struct{}{}
	}
	var text string
	for _, c := range content {
		if _, ok := accept[c.Type]; ok && c.Text != "" {
			text += c.Text
		}
	}
	return text
}

type codexUserShellCommand struct {
	Command         string
	ExitCode        int
	DurationSeconds float64
	Output          string
}

func parseCodexUserShellCommand(text string) (codexUserShellCommand, bool) {
	const (
		wrapperOpen  = "<user_shell_command>"
		wrapperClose = "</user_shell_command>"
	)

	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, wrapperOpen) || !strings.HasSuffix(text, wrapperClose) {
		return codexUserShellCommand{}, false
	}
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(text, wrapperOpen), wrapperClose))
	command, ok := codexWrappedSection(body, "command")
	if !ok || strings.TrimSpace(command) == "" {
		return codexUserShellCommand{}, false
	}
	result, ok := codexWrappedSection(body, "result")
	if !ok {
		return codexUserShellCommand{}, false
	}

	lines := strings.Split(strings.TrimSpace(result), "\n")
	if len(lines) < 3 {
		return codexUserShellCommand{}, false
	}
	exitText, ok := strings.CutPrefix(strings.TrimSuffix(lines[0], "\r"), "Exit code:")
	if !ok {
		return codexUserShellCommand{}, false
	}
	exitCode, err := strconv.Atoi(strings.TrimSpace(exitText))
	if err != nil {
		return codexUserShellCommand{}, false
	}
	durationText, ok := strings.CutPrefix(strings.TrimSuffix(lines[1], "\r"), "Duration:")
	if !ok {
		return codexUserShellCommand{}, false
	}
	durationText = strings.TrimSpace(durationText)
	durationText, ok = strings.CutSuffix(durationText, " seconds")
	if !ok {
		return codexUserShellCommand{}, false
	}
	durationSeconds, err := strconv.ParseFloat(strings.TrimSpace(durationText), 64)
	if err != nil {
		return codexUserShellCommand{}, false
	}
	if strings.TrimSuffix(lines[2], "\r") != "Output:" {
		return codexUserShellCommand{}, false
	}

	return codexUserShellCommand{
		Command:         strings.TrimSpace(command),
		ExitCode:        exitCode,
		DurationSeconds: durationSeconds,
		Output:          strings.TrimSpace(strings.Join(lines[3:], "\n")),
	}, true
}

func codexWrappedSection(text, name string) (string, bool) {
	open := "<" + name + ">"
	close := "</" + name + ">"
	start := strings.Index(text, open)
	if start < 0 {
		return "", false
	}
	start += len(open)
	end := strings.Index(text[start:], close)
	if end < 0 {
		return "", false
	}
	return text[start : start+end], true
}

func buildCodexUserShellCommandUse(cmd codexUserShellCommand, event CodexEvent, cwd, sessionID, recordType string) ToolUse {
	status := "success"
	if cmd.ExitCode != 0 {
		status = "failed"
	}
	input := map[string]any{
		"event":            "user_shell_command",
		"command":          cmd.Command,
		"exit_code":        cmd.ExitCode,
		"duration_seconds": cmd.DurationSeconds,
		"duration_ms":      cmd.DurationSeconds * 1000,
		"output":           cmd.Output,
		"stdout":           cmd.Output,
		"status":           status,
	}
	return ToolUse{
		Tool:       "UserShellCommand",
		Input:      input,
		Timestamp:  event.Time(),
		CWD:        cwd,
		SessionID:  sessionID,
		TurnID:     codexEventTurnID(event),
		Source:     "codex",
		Response:   cmd.Output,
		RecordType: recordType,
	}
}

func isCodexInternalUserText(text string) bool {
	text = strings.TrimSpace(text)
	return strings.HasPrefix(text, "<environment_context>") ||
		strings.HasPrefix(text, "<developer_context>") ||
		strings.HasPrefix(text, "<recommended_plugins>") ||
		strings.HasPrefix(text, "<plugins_instructions>") ||
		strings.HasPrefix(text, "<skills_instructions>")
}

func codexUserMessageTool(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	if strings.HasPrefix(text, "# AGENTS.md") ||
		(strings.HasPrefix(text, "<recommended_plugins>") && strings.Contains(text, "# AGENTS.md instructions")) {
		return "System", true
	}
	if isCodexInternalUserText(text) {
		return "", false
	}
	return "User", true
}

func codexEventTurnID(event CodexEvent) string {
	if event.Payload.TurnID != "" {
		return event.Payload.TurnID
	}
	if event.Payload.Metadata != nil {
		return event.Payload.Metadata.TurnID
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func buildCodexEventUse(event CodexEvent, cwd, sessionID string) ToolUse {
	input := make(map[string]any, len(event.Payload.Raw)+1)
	for k, v := range event.Payload.Raw {
		input[k] = v
	}
	input["event"] = event.Payload.Type
	add := func(key string, value any) {
		switch v := value.(type) {
		case string:
			if v != "" {
				input[key] = v
			}
		case int:
			if v != 0 {
				input[key] = v
			}
		case int64:
			if v != 0 {
				input[key] = v
			}
		case float64:
			if v != 0 {
				input[key] = v
			}
		}
	}
	add("turn_id", event.Payload.TurnID)
	add("message", event.Payload.Message)
	add("phase", event.Payload.Phase)
	add("started_at", event.Payload.StartedAt)
	add("completed_at", event.Payload.CompletedAt)
	add("duration_ms", event.Payload.DurationMS)
	add("time_to_first_token_ms", event.Payload.TimeToFirstTokenMS)
	add("last_agent_message", event.Payload.LastAgentMessage)
	add("model_context_window", event.Payload.ModelContextWindow)
	add("collaboration_mode_kind", event.Payload.CollaborationModeKind)
	if event.Payload.Info != nil {
		input["total_tokens"] = event.Payload.Info.LastTokenUsage.TotalTokens
		input["input_tokens"] = event.Payload.Info.LastTokenUsage.InputTokens
		input["output_tokens"] = event.Payload.Info.LastTokenUsage.OutputTokens
		input["cached_input_tokens"] = event.Payload.Info.LastTokenUsage.CachedInputTokens
		input["cumulative_total_tokens"] = event.Payload.Info.TotalTokenUsage.TotalTokens
		input["cumulative_input_tokens"] = event.Payload.Info.TotalTokenUsage.InputTokens
		input["cumulative_output_tokens"] = event.Payload.Info.TotalTokenUsage.OutputTokens
		input["cumulative_cached_input_tokens"] = event.Payload.Info.TotalTokenUsage.CachedInputTokens
		if event.Payload.Info.LastTokenUsage.ReasoningOutputTokens != 0 {
			input["reasoning_output_tokens"] = event.Payload.Info.LastTokenUsage.ReasoningOutputTokens
		}
		if event.Payload.Info.ModelContextWindow != 0 {
			input["model_context_window"] = event.Payload.Info.ModelContextWindow
		}
	}
	usage := eventTokenUsage(event)
	return ToolUse{
		Tool:            tools.EventToolName(event.Payload.Type),
		Input:           input,
		Timestamp:       event.Time(),
		CWD:             cwd,
		SessionID:       sessionID,
		TurnID:          codexEventTurnID(event),
		Source:          "codex",
		InputTokens:     codexNonCachedInputTokens(usage),
		OutputTokens:    usage.OutputTokens,
		CacheReadTokens: usage.CachedInputTokens,
		TotalTokens:     usage.TotalTokens,
		ContextWindow:   eventContextWindow(event),
		RecordType:      "event_msg." + event.Payload.Type,
	}
}

func buildCodexTopLevelEventUse(event CodexEvent, eventType, cwd, sessionID string) ToolUse {
	input := make(map[string]any, len(event.Payload.Raw)+1)
	for k, v := range event.Payload.Raw {
		input[k] = v
	}
	input["event"] = eventType
	return ToolUse{
		Tool:       tools.EventToolName(eventType),
		Input:      input,
		Timestamp:  event.Time(),
		CWD:        cwd,
		SessionID:  sessionID,
		TurnID:     codexEventTurnID(event),
		Source:     "codex",
		RecordType: event.Type,
	}
}

func eventTokenUsage(event CodexEvent) CodexTokenUsage {
	if event.Payload.Info == nil {
		return CodexTokenUsage{}
	}
	return event.Payload.Info.LastTokenUsage
}

func eventContextWindow(event CodexEvent) int {
	if event.Payload.Info == nil {
		return 0
	}
	return event.Payload.Info.ModelContextWindow
}

func codexNonCachedInputTokens(usage CodexTokenUsage) int {
	if usage.CachedInputTokens <= 0 {
		return usage.InputTokens
	}
	input := usage.InputTokens - usage.CachedInputTokens
	if input < 0 {
		return usage.InputTokens
	}
	return input
}

func dedupeCodexToolUses(uses []ToolUse) []ToolUse {
	var out []ToolUse
	seen := map[string]int{}
	priorities := map[string]int{}
	for _, use := range uses {
		key, ok := codexChatDedupeKey(use)
		if !ok {
			out = append(out, use)
			continue
		}
		priority := codexRecordPriority(use.RecordType)
		if idx, exists := seen[key]; exists {
			if priority > priorities[key] {
				out[idx] = use
				priorities[key] = priority
			}
			continue
		}
		seen[key] = len(out)
		priorities[key] = priority
		out = append(out, use)
	}
	return out
}

func codexChatDedupeKey(use ToolUse) (string, bool) {
	switch use.Tool {
	case "System", "User", "Assistant", "Reasoning", "Plan", "MemoryCitation":
	default:
		return "", false
	}
	if use.Timestamp == nil {
		return "", false
	}
	text := codexDedupeText(use)
	if text == "" {
		return "", false
	}
	return strings.Join([]string{
		use.SessionID,
		use.Tool,
		use.Timestamp.UTC().Format(time.RFC3339Nano),
		text,
	}, "\x00"), true
}

func codexDedupeText(use ToolUse) string {
	if text, ok := use.Input["text"].(string); ok {
		return strings.TrimSpace(text)
	}
	if content, ok := use.Input["content"].(string); ok {
		return strings.TrimSpace(content)
	}
	if use.Tool == "MemoryCitation" {
		return fmt.Sprint(use.Input["citation_entries"], "\x00", use.Input["rollout_ids"])
	}
	return ""
}

func codexRecordPriority(recordType string) int {
	switch {
	case strings.HasPrefix(recordType, "response_item."):
		return 3
	case strings.HasPrefix(recordType, "item.completed"):
		return 2
	case strings.HasPrefix(recordType, "event_msg."):
		return 1
	default:
		return 0
	}
}

func buildToolUse(callEvent, outputEvent CodexEvent, cwd, sessionID string) ToolUse {
	response := extractCommandOutput(CodexOutputText(outputEvent.Payload.Output))
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
			TurnID:    firstNonEmpty(codexEventTurnID(callEvent), codexEventTurnID(outputEvent)),
			ToolUseID: callEvent.Payload.CallID,
			Source:    "codex",
			Response:  response,
			Namespace: callEvent.Payload.Namespace,
		}
	case "request_user_input":
		return ToolUse{
			Tool:      "AskUserQuestion",
			Input:     extractArgumentsMap(callEvent.Payload.Arguments),
			Timestamp: ts,
			CWD:       cwd,
			SessionID: sessionID,
			TurnID:    firstNonEmpty(codexEventTurnID(callEvent), codexEventTurnID(outputEvent)),
			ToolUseID: callEvent.Payload.CallID,
			Source:    "codex",
			Response:  response,
			Namespace: callEvent.Payload.Namespace,
		}
	case "wait":
		return ToolUse{
			Tool:       "Wait",
			Input:      extractArgumentsMap(callEvent.Payload.Arguments),
			Timestamp:  ts,
			CWD:        cwd,
			SessionID:  sessionID,
			TurnID:     firstNonEmpty(codexEventTurnID(callEvent), codexEventTurnID(outputEvent)),
			ToolUseID:  callEvent.Payload.CallID,
			Source:     "codex",
			Response:   response,
			Namespace:  callEvent.Payload.Namespace,
			RecordType: "response_item.function_call",
		}
	case "spawn_agent":
		args := extractArgumentsMap(callEvent.Payload.Arguments)
		agentType, _ := args["agent_type"].(string)
		desc, _ := args["message"].(string)
		input := map[string]any{}
		for k, v := range args {
			input[k] = v
		}
		input["subagent_type"] = agentType
		input["description"] = desc
		input["prompt"] = desc
		agentID, nickname := codexAgentOutput(response)
		if agentID != "" {
			input["agent_id"] = agentID
		}
		if nickname != "" {
			input["nickname"] = nickname
		}
		return ToolUse{
			Tool:       "Agent",
			Input:      input,
			Timestamp:  ts,
			CWD:        cwd,
			SessionID:  sessionID,
			TurnID:     firstNonEmpty(codexEventTurnID(callEvent), codexEventTurnID(outputEvent)),
			ToolUseID:  callEvent.Payload.CallID,
			Source:     "codex",
			Response:   response,
			Namespace:  callEvent.Payload.Namespace,
			AgentID:    agentID,
			AgentType:  agentType,
			AgentDesc:  desc,
			RecordType: "response_item.function_call",
		}
	case "wait_agent":
		return codexNamedCallUse("CollabWaiting", callEvent, outputEvent, ts, cwd, sessionID, response)
	case "close_agent":
		return codexNamedCallUse("CollabClose", callEvent, outputEvent, ts, cwd, sessionID, response)
	case "send_input", "resume_agent":
		return codexNamedCallUse("CollabAgentInteraction", callEvent, outputEvent, ts, cwd, sessionID, response)
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
		TurnID:    firstNonEmpty(codexEventTurnID(callEvent), codexEventTurnID(outputEvent)),
		ToolUseID: callEvent.Payload.CallID,
		Source:    "codex",
		Response:  response,
		Namespace: callEvent.Payload.Namespace,
	}
}

func codexNamedCallUse(tool string, callEvent, outputEvent CodexEvent, ts *time.Time, cwd, sessionID, response string) ToolUse {
	input := extractArgumentsMap(callEvent.Payload.Arguments)
	input["event"] = callEvent.Payload.Name
	return ToolUse{
		Tool:       tool,
		Input:      input,
		Timestamp:  ts,
		CWD:        cwd,
		SessionID:  sessionID,
		TurnID:     firstNonEmpty(codexEventTurnID(callEvent), codexEventTurnID(outputEvent)),
		ToolUseID:  callEvent.Payload.CallID,
		Source:     "codex",
		Response:   response,
		Namespace:  callEvent.Payload.Namespace,
		RecordType: "response_item.function_call",
	}
}

func codexAgentOutput(response string) (agentID, nickname string) {
	var payload struct {
		AgentID  string `json:"agent_id"`
		Nickname string `json:"nickname"`
	}
	if json.Unmarshal([]byte(strings.TrimSpace(response)), &payload) == nil {
		return payload.AgentID, payload.Nickname
	}
	return "", ""
}

func extractArgumentsMap(argsJSON json.RawMessage) map[string]any {
	raw := normalizeCodexArguments(argsJSON)
	var args map[string]any
	if raw == "" || json.Unmarshal([]byte(raw), &args) != nil {
		return map[string]any{}
	}
	return args
}

func extractCommand(argsJSON json.RawMessage) string {
	raw := normalizeCodexArguments(argsJSON)
	if raw == "" {
		return ""
	}
	var args struct {
		Cmd string `json:"cmd"`
	}
	if json.Unmarshal([]byte(raw), &args) == nil && args.Cmd != "" {
		return args.Cmd
	}
	return raw
}

func normalizeCodexArguments(argsJSON json.RawMessage) string {
	if len(argsJSON) == 0 || string(argsJSON) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(argsJSON, &s) == nil {
		return s
	}
	return string(argsJSON)
}

// CodexOutputText normalizes the scalar-string and ordered content-block forms
// Codex uses for function_call_output records. Provider streaming and history
// ingestion share it so the same output is rendered in both paths.
func CodexOutputText(raw json.RawMessage) string {
	text := strings.TrimSpace(string(raw))
	if text == "" || text == "null" {
		return ""
	}

	var scalar string
	if json.Unmarshal(raw, &scalar) == nil {
		return scalar
	}

	var content []CodexContent
	if json.Unmarshal(raw, &content) == nil {
		if combined := codexContentText(content, "input_text", "output_text", "text"); combined != "" {
			return combined
		}
	}

	// Preserve unfamiliar non-null shapes instead of dropping the completion.
	// RawMessage already contains compact JSON from the transcript.
	return text
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
