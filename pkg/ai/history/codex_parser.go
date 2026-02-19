package history

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/flanksource/commons/logger"
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
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)

	var (
		toolUses    []ToolUse
		sessionCWD  string
		sessionID   string
		pendingCall = make(map[string]CodexEvent)
	)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		event, err := ParseCodexLine(line)
		if err != nil {
			logger.Debugf("Error parsing codex line in %s: %v", sessionFile, err)
			continue
		}

		switch event.Type {
		case "session_meta":
			sessionCWD = event.Payload.CWD
			sessionID = event.Payload.ID

		case "response_item":
			toolUses = append(toolUses, extractResponseItem(event, pendingCall, sessionCWD, sessionID)...)

		case "event_msg":
			toolUses = append(toolUses, extractEventMsg(event, sessionCWD, sessionID)...)
		}
	}

	for _, callEvent := range pendingCall {
		toolUses = append(toolUses, buildToolUse(callEvent, CodexEvent{}, sessionCWD, sessionID))
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
		var text string
		for _, s := range event.Payload.Summary {
			if s.Text != "" {
				text = s.Text
			}
		}
		if text == "" {
			return nil
		}
		return []ToolUse{{
			Tool:      "CodexReasoning",
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
			Tool:      "CodexMessage",
			Input:     map[string]any{"text": text},
			Timestamp: event.Time(),
			CWD:       cwd,
			SessionID: sessionID,
			Source:    "codex",
		}}
	}
	return nil
}

func extractEventMsg(event CodexEvent, cwd, sessionID string) []ToolUse {
	switch event.Payload.Type {
	case "agent_reasoning":
		if event.Payload.Text == "" {
			return nil
		}
		return []ToolUse{{
			Tool:      "CodexReasoning",
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
			Tool:      "CodexMessage",
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
	input := map[string]any{
		"command": extractCommand(callEvent.Payload.Arguments),
	}
	if outputEvent.Payload.Output != "" {
		input["output"] = extractCommandOutput(outputEvent.Payload.Output)
	}
	ts := callEvent.Time()
	if ts == nil {
		ts = outputEvent.Time()
	}
	return ToolUse{
		Tool:      "CodexCommand",
		Input:     input,
		Timestamp: ts,
		CWD:       cwd,
		SessionID: sessionID,
		ToolUseID: callEvent.Payload.CallID,
		Source:    "codex",
	}
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
