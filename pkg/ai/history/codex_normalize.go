package history

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/bash"
	"github.com/flanksource/captain/pkg/claude/tools"
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
// tool shape. A freeform `exec` script can describe several operations, so this
// returns only the first of them; callers that render a transcript want
// NormalizeCodexToolCalls.
func NormalizeCodexToolCall(call CodexToolCall) ToolUse {
	uses := NormalizeCodexToolCalls(call)
	if len(uses) == 0 {
		return ToolUse{}
	}
	return uses[0]
}

// NormalizeCodexToolCalls maps a native Codex call into every row it describes.
// Only the freeform `exec` tool yields more than one: it takes a JavaScript
// program rather than a command, and one program routinely runs a handful of
// shell commands and applies a patch.
func NormalizeCodexToolCalls(call CodexToolCall) []ToolUse {
	input := codexCallInput(call)
	script, isScript := codexFreeformScript(call, input)
	if !isScript {
		return []ToolUse{normalizeCodexCall(call, input)}
	}
	invocations, err := parseCodexExecScript(script)
	if err != nil {
		return []ToolUse{codexExecScriptUse(call, script, err)}
	}
	if len(invocations) == 0 {
		// A script that only prints. It ran, so it earns a row; it just has no
		// operation to name.
		return []ToolUse{codexExecScriptUse(call, script, nil)}
	}
	return codexScriptRows(call, script, invocations)
}

func codexCallInput(call CodexToolCall) map[string]any {
	input := codexArgumentsMap(call.Arguments)
	for key, value := range call.Input {
		input[key] = value
	}
	return input
}

// codexFreeformScript returns the JavaScript program behind a freeform `exec`
// call. The same tool name also arrives with a plain command, which is not a
// script and must not be parsed as one.
func codexFreeformScript(call CodexToolCall, input map[string]any) (string, bool) {
	if call.Name != "exec" || call.Command != "" {
		return "", false
	}
	if stringValue(input["command"]) != "" || stringValue(input["cmd"]) != "" {
		return "", false
	}
	script, _ := input["input"].(string)
	return script, script != ""
}

func normalizeCodexCall(call CodexToolCall, input map[string]any) ToolUse {
	command := firstNonEmpty(call.Command, stringValue(input["command"]), stringValue(input["cmd"]))
	cwd := call.CWD
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
	case "apply_patch":
		tool = codexApplyPatchTool(input)
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
	if tool == "Bash" {
		input = bash.TransformBashInput(input)
	}

	use := ToolUse{
		Tool:            tool,
		Input:           input,
		Timestamp:       call.Timestamp,
		CWD:             cwd,
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

// codexApplyPatchTool rewrites a patch payload into the file operation it really
// is and returns the tool that renders it. A single-file patch becomes the same
// Write or Edit row the Claude side produces -- 82% of them are single-file --
// so it shows a path and a diff rather than a blob of patch syntax.
func codexApplyPatchTool(input map[string]any) string {
	patch := firstNonEmpty(
		stringValue(input["input"]),
		stringValue(input["patch"]),
		stringValue(input["arguments"]),
	)
	files := tools.ParseApplyPatch(patch)
	if len(files) == 0 {
		return "ApplyPatch"
	}
	input["files"] = files
	if len(files) > 1 {
		return "ApplyPatch"
	}

	file := files[0]
	input["file_path"] = file.Path
	switch {
	case file.MoveTo != "":
		input["operation"] = "move"
		input["to"] = file.MoveTo
		return "ApplyPatch"
	case file.Op == tools.ApplyPatchDelete:
		input["operation"] = "delete"
		return "ApplyPatch"
	case file.Op == tools.ApplyPatchAdd:
		input["content"] = file.Content
		return "Write"
	default:
		input["old_string"] = file.Old
		input["new_string"] = file.New
		return "Edit"
	}
}

// codexScriptRows turns a parsed freeform exec script into transcript rows. All
// of a script's shell commands merge into one Bash row -- they read as one shell
// block, which is how the model wrote them -- while every other tool it invokes
// gets the same first-class row its function_call form already produces.
func codexScriptRows(call CodexToolCall, script string, invocations []codexScriptCall) []ToolUse {
	commands := make([]codexExecCommand, 0, len(invocations))
	for _, invocation := range invocations {
		if invocation.Name != codexExecCommandTool {
			continue
		}
		command, err := codexExecCommandFrom(invocation)
		if err != nil {
			return []ToolUse{codexExecScriptUse(call, script, err)}
		}
		commands = append(commands, command)
	}
	merged, workdir, err := renderCodexExecCommands(commands, call.CWD)
	if err != nil {
		return []ToolUse{codexExecScriptUse(call, script, err)}
	}

	rows := make([]ToolUse, 0, len(invocations))
	shellRow := -1
	for _, invocation := range invocations {
		nested := call
		nested.Arguments = nil
		nested.Input = nil
		nested.Response = ""
		if invocation.Name == codexExecCommandTool {
			if shellRow >= 0 {
				continue
			}
			shellRow = len(rows)
			nested.Name = ""
			nested.Command = merged
			nested.CWD = workdir
			// The script stays on the shell row as provenance: it is what the
			// model actually wrote, and the merged command is a rendering of it.
			rows = append(rows, normalizeCodexCall(nested, map[string]any{"input": script}))
			continue
		}
		nested.Name = invocation.Name
		nested.Command = ""
		rows = append(rows, normalizeCodexCall(nested, maps.Clone(invocation.Args)))
	}
	return codexScriptRowIdentity(rows, call, shellRow)
}

// codexScriptRowIdentity gives every row after the first its own tool-use id --
// they all came from one call_id, and a shared id would collapse them downstream
// -- and attaches the script's output to the row that produced it.
func codexScriptRowIdentity(rows []ToolUse, call CodexToolCall, shellRow int) []ToolUse {
	if len(rows) == 0 {
		return rows
	}
	responseRow := shellRow
	if responseRow < 0 {
		responseRow = len(rows) - 1
	}
	for index := range rows {
		if index > 0 && call.ID != "" {
			rows[index].ToolUseID = fmt.Sprintf("%s#%d", call.ID, index)
		}
		if index == responseRow {
			rows[index].Response = call.Response
		}
	}
	return rows
}

// codexExecScriptUse is the row for a freeform script that resolved to no
// operation. The script itself is the content -- rendering it as an opaque JSON
// dump, or worse as a command with `{{js:…}}` mustaches in it, is what made the
// parser's failures invisible.
func codexExecScriptUse(call CodexToolCall, script string, err error) ToolUse {
	input := map[string]any{"script": script}
	if err != nil {
		input["parse_error"] = err.Error()
	}
	return ToolUse{
		Tool:            "CodexExecScript",
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
}

// buildToolUses maps a call record and its output record into rows. One record
// pair is usually one row, but a freeform `exec` call is a JavaScript program
// that can invoke several tools, and each invocation earns its own row.
func buildToolUses(callEvent, outputEvent CodexEvent, cwd, sessionID string) []ToolUse {
	timestamp := callEvent.Time()
	if timestamp == nil {
		timestamp = outputEvent.Time()
	}
	var input map[string]any
	if callEvent.Payload.Input != "" {
		input = map[string]any{"input": callEvent.Payload.Input}
	}
	return NormalizeCodexToolCalls(CodexToolCall{
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

// withSourceLine stamps rows with the JSONL line that produced them. Rows are
// keyed on it downstream, so it must be the line of the record that opened the
// row -- the call, not the output that completes it several lines later.
func withSourceLine(uses []ToolUse, line int64) []ToolUse {
	for index := range uses {
		uses[index].SourceLine = line
	}
	return uses
}

// asProvisional marks rows a later pass can still complete, so ingest keeps
// offering them instead of sealing a half-written row forever.
func asProvisional(uses []ToolUse) []ToolUse {
	for index := range uses {
		uses[index].Provisional = true
	}
	return uses
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
