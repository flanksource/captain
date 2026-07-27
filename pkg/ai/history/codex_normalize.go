package history

import (
	"strings"
	"time"

	"github.com/segmentio/encoding/json"
)

// CodexToolCall is the transport-neutral input for Codex tool normalization.
// Rollout JSONL, codex exec JSON, and app-server notifications decode their
// native envelopes independently and converge on this shape.
type CodexToolCall struct {
	Name            string
	Namespace       string
	Arguments       json.RawMessage
	Command         string
	Input           map[string]any
	Timestamp       *time.Time
	CWD             string
	SessionID       string
	TurnID          string
	ID              string
	Response        string
	Model           string
	ReasoningEffort string
	RecordType      string
}

// NormalizeCodexToolCall maps a native Codex call into the canonical history
// tool shape consumed by both live providers and transcript rendering.
func NormalizeCodexToolCall(call CodexToolCall) ToolUse {
	input := codexArgumentsMap(call.Arguments)
	for key, value := range call.Input {
		input[key] = value
	}

	command := firstNonEmpty(call.Command, stringValue(input["command"]), stringValue(input["cmd"]))
	if command == "" && call.Name == "" {
		command = codexScalarArgument(call.Arguments)
		delete(input, "arguments")
	}
	if command != "" {
		delete(input, "cmd")
		input["command"] = command
	}

	tool := call.Name
	switch call.Name {
	case "update_plan":
		tool = "TodoWrite"
		if plan, ok := input["plan"]; ok {
			input["todos"] = plan
		}
	case "request_user_input":
		tool = "AskUserQuestion"
	case "wait":
		tool = "Wait"
	case "spawn_agent":
		tool = "Agent"
	case "wait_agent":
		tool = "CollabWaiting"
		input["event"] = call.Name
	case "close_agent":
		tool = "CollabClose"
		input["event"] = call.Name
	case "send_input", "resume_agent":
		tool = "CollabAgentInteraction"
		input["event"] = call.Name
	default:
		if command != "" {
			tool = "Bash"
		}
	}
	if tool == "" {
		tool = "Bash"
	}

	use := ToolUse{
		Tool:            tool,
		Input:           input,
		Timestamp:       call.Timestamp,
		CWD:             call.CWD,
		SessionID:       call.SessionID,
		TurnID:          call.TurnID,
		ToolUseID:       call.ID,
		Source:          "codex",
		Model:           call.Model,
		ReasoningEffort: call.ReasoningEffort,
		Namespace:       call.Namespace,
		Response:        call.Response,
		RecordType:      call.RecordType,
	}
	if tool == "Agent" {
		use.AgentType, _ = input["agent_type"].(string)
		use.AgentDesc, _ = input["message"].(string)
		input["subagent_type"] = use.AgentType
		input["description"] = use.AgentDesc
		input["prompt"] = use.AgentDesc
		agentID, nickname := codexAgentOutput(call.Response)
		use.AgentID = agentID
		if use.AgentID != "" {
			input["agent_id"] = use.AgentID
		}
		if nickname != "" {
			input["nickname"] = nickname
		}
	}
	return use
}

func buildToolUse(callEvent, outputEvent CodexEvent, cwd, sessionID string) ToolUse {
	timestamp := callEvent.Time()
	if timestamp == nil {
		timestamp = outputEvent.Time()
	}
	var input map[string]any
	if callEvent.Payload.Input != "" {
		input = map[string]any{"input": callEvent.Payload.Input}
	}
	return NormalizeCodexToolCall(CodexToolCall{
		Name:       callEvent.Payload.Name,
		Namespace:  callEvent.Payload.Namespace,
		Arguments:  callEvent.Payload.Arguments,
		Input:      input,
		Timestamp:  timestamp,
		CWD:        cwd,
		SessionID:  sessionID,
		TurnID:     firstNonEmpty(codexEventTurnID(callEvent), codexEventTurnID(outputEvent)),
		ID:         callEvent.Payload.CallID,
		Response:   extractCommandOutput(CodexOutputText(outputEvent.Payload.Output)),
		RecordType: "response_item." + callEvent.Payload.Type,
	})
}

func codexArgumentsMap(arguments json.RawMessage) map[string]any {
	raw := normalizeCodexArguments(arguments)
	var input map[string]any
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &input)
	}
	if input != nil {
		return input
	}

	var value any
	if len(arguments) > 0 && string(arguments) != "null" && json.Unmarshal(arguments, &value) == nil {
		return map[string]any{"arguments": value}
	}
	return map[string]any{}
}

func extractArgumentsMap(arguments json.RawMessage) map[string]any {
	return codexArgumentsMap(arguments)
}

func normalizeCodexArguments(arguments json.RawMessage) string {
	if len(arguments) == 0 || string(arguments) == "null" {
		return ""
	}
	var value string
	if json.Unmarshal(arguments, &value) == nil {
		return value
	}
	return string(arguments)
}

func codexScalarArgument(arguments json.RawMessage) string {
	raw := normalizeCodexArguments(arguments)
	if raw == "" || strings.HasPrefix(strings.TrimSpace(raw), "{") {
		return ""
	}
	return raw
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
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

// NormalizeCodexError unwraps the JSON-encoded error envelope Codex sometimes
// stores inside a message field.
func NormalizeCodexError(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return raw
	}
	var inner CodexErrorBlock
	wrapper := struct {
		Error   *CodexErrorBlock `json:"error"`
		Message string           `json:"message"`
	}{Error: &inner}
	if json.Unmarshal([]byte(raw), &wrapper) != nil {
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

// CodexOutputText normalizes the scalar-string and ordered content-block forms
// Codex uses for function-call output records.
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
	return text
}

func extractCommandOutput(raw string) string {
	if _, after, ok := strings.Cut(raw, "Output:\n"); ok {
		return after
	}
	return raw
}
