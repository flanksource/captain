package history

import (
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/ai/assistanttags"
)

// codexPendingCall is a tool call awaiting its output, kept with the line it
// arrived on so the merged row keeps the CALL's identity. Keying the merged row
// on the output line instead would give the row a different identity than the
// unpaired row a previous pass wrote, and the correction would never land.
type codexPendingCall struct {
	event CodexEvent
	line  int64
}

func extractResponseItem(event CodexEvent, pendingCall map[string]codexPendingCall, cwd, sessionID string, line int64) []ToolUse {
	switch event.Payload.Type {
	case "function_call", "custom_tool_call", "tool_search_call":
		pendingCall[event.Payload.CallID] = codexPendingCall{event: event, line: line}
		return nil
	case "function_call_output", "custom_tool_call_output":
		call, ok := pendingCall[event.Payload.CallID]
		if !ok {
			return nil
		}
		delete(pendingCall, event.Payload.CallID)
		return withSourceLine(buildToolUses(call.event, event, cwd, sessionID), call.line)
	case "tool_search_output":
		call, ok := pendingCall[event.Payload.CallID]
		if !ok {
			return nil
		}
		delete(pendingCall, event.Payload.CallID)
		return withSourceLine(buildToolSearchUses(call.event, event, cwd, sessionID), call.line)
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
		case "developer":
			// A developer message is briefing, never conversation: the
			// primary-agent instructions, memory guidance and multi-agent mode
			// rules the model was actually given. The default case below used to
			// drop it unconditionally, so none of that context reached the
			// transcript. It is a System row, and unlike user text it is never
			// filtered as "internal" — being internal is the whole point of it.
			text = codexContentText(event.Payload.Content, "input_text", "text")
			tool = "System"
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

func extractLiveItemCompleted(event CodexEvent, cwd, sessionID string) []ToolUse {
	if event.Item == nil || event.Item.Role != "" && event.Item.Role != "assistant" {
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

func extractLiveError(event CodexEvent, cwd, sessionID string) []ToolUse {
	var message string
	if event.Error != nil {
		message = event.Error.Message
	}
	if message == "" {
		message = event.Message
	}
	message = NormalizeCodexError(message)
	if message == "" {
		return nil
	}
	return []ToolUse{{
		Tool:      "ApiError",
		Input:     map[string]any{"error": message},
		Timestamp: event.Time(),
		CWD:       cwd,
		SessionID: sessionID,
		TurnID:    codexEventTurnID(event),
		Source:    "codex",
	}}
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
		text := firstNonEmpty(event.Payload.Message, event.Payload.Text)
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
	return []ToolUse{codexToolSearchUse(callEvent, outputEvent, names, cwd, sessionID)}
}

// buildPendingToolSearchUse is the row for a tool_search_call still waiting for
// its output at EOF. The tools it resolved to are not known yet, but the row has
// to exist: it is keyed on the call's line, and ingest's high-water mark stops
// below the lowest provisional row, which is what lets a later pass overwrite it
// with the completed pair. It also has to be a DeferredToolsDelta, because a
// tool_search_call carries no Name and buildToolUses would normalize it into a
// bogus command row that the completed row then contradicts.
func buildPendingToolSearchUse(callEvent CodexEvent, cwd, sessionID string) []ToolUse {
	return []ToolUse{codexToolSearchUse(callEvent, CodexEvent{}, nil, cwd, sessionID)}
}

// codexToolSearchUse builds the DeferredToolsDelta row shared by the completed
// pair and the still-pending call, so the provisional row a tail parse writes has
// the same identity as the row that replaces it.
func codexToolSearchUse(callEvent, outputEvent CodexEvent, names []string, cwd, sessionID string) ToolUse {
	input := map[string]any{"event": "deferred_tools_delta"}
	if len(names) > 0 {
		input["addedNames"] = names
	}
	for key, value := range extractArgumentsMap(callEvent.Payload.Arguments) {
		input[key] = value
	}
	timestamp := outputEvent.Time()
	if timestamp == nil {
		timestamp = callEvent.Time()
	}
	return ToolUse{
		Tool:       "DeferredToolsDelta",
		Input:      input,
		Timestamp:  timestamp,
		CWD:        cwd,
		SessionID:  sessionID,
		TurnID:     firstNonEmpty(codexEventTurnID(outputEvent), codexEventTurnID(callEvent)),
		ToolUseID:  firstNonEmpty(outputEvent.Payload.CallID, callEvent.Payload.CallID),
		Source:     "codex",
		RecordType: "response_item." + firstNonEmpty(outputEvent.Payload.Type, callEvent.Payload.Type),
	}
}

func codexToolSearchNames(groups []CodexToolSearchNamespace) []string {
	var names []string
	seen := map[string]struct{}{}
	for _, group := range groups {
		for _, tool := range group.Tools {
			name := strings.TrimSpace(tool.Name)
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			names = append(names, name)
		}
	}
	return names
}

func codexContentText(content []CodexContent, accepted ...string) string {
	accept := make(map[string]struct{}, len(accepted))
	for _, contentType := range accepted {
		accept[contentType] = struct{}{}
	}
	var text string
	for _, item := range content {
		if _, ok := accept[item.Type]; ok && item.Text != "" {
			text += item.Text
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
	const wrapperOpen = "<user_shell_command>"
	const wrapperClose = "</user_shell_command>"
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
	durationText, ok = strings.CutSuffix(strings.TrimSpace(durationText), " seconds")
	if !ok {
		return codexUserShellCommand{}, false
	}
	durationSeconds, err := strconv.ParseFloat(strings.TrimSpace(durationText), 64)
	if err != nil || strings.TrimSuffix(lines[2], "\r") != "Output:" {
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
		strings.HasPrefix(text, "<recommended_plugins>") && strings.Contains(text, "# AGENTS.md instructions") {
		return "System", true
	}
	if isCodexInternalUserText(text) {
		return "", false
	}
	return "User", true
}
