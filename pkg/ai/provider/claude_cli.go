package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
)

var claudeCLICommand = "claude"

type ClaudeCLI struct {
	model   string
	sandbox *api.SandboxConfig
}

func NewClaudeCLI(model string) *ClaudeCLI {
	if strings.TrimSpace(model) == "" {
		model = "opus"
	}
	model = ai.NormalizeModelForBackend(ai.BackendClaudeCLI, model)
	return &ClaudeCLI{model: model}
}

func (c *ClaudeCLI) GetModel() string       { return c.model }
func (c *ClaudeCLI) GetBackend() ai.Backend { return ai.BackendClaudeCLI }

func (c *ClaudeCLI) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()
	events, err := c.ExecuteStream(ctx, req)
	if err != nil {
		return nil, err
	}
	resp, err := CoalesceStreamForBackend(ctx, ai.BackendClaudeCLI, c.model, events, start)
	if err != nil {
		return nil, err
	}
	if req.Prompt.Schema != nil {
		raw, _ := resp.StructuredData.(json.RawMessage)
		if len(raw) == 0 {
			raw = json.RawMessage(resp.Text)
		}
		if err := ai.BindStructuredOutput(req.Prompt.Schema, raw); err != nil {
			return nil, err
		}
		resp.StructuredData = req.Prompt.Schema
		resp.Text = ""
	}
	return resp, nil
}

func (c *ClaudeCLI) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	args, cleanup, err := buildClaudeCLIArgs(c.model, req)
	if err != nil {
		return nil, err
	}
	cmd, stdout, stderrBuf, closeSandbox, err := startCLIStream(ctx, claudeCLICommand, args, []byte(composePrompt(req)), &req, c.sandbox)
	if err != nil {
		cleanup()
		return nil, err
	}
	out := make(chan ai.Event, 16)
	go func() {
		defer close(out)
		defer cleanup()
		defer closeSandbox()
		defer func() { _ = stdout.Close() }()
		iterator := claude.NewStreamJSONIterator(stdout)
		for iterator.Next() {
			for _, ev := range claudeEntryEvents(iterator.Entry(), c.model) {
				emit(ctx, out, ev)
			}
		}
		if err := iterator.Err(); err != nil {
			emit(ctx, out, ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("claude stream-json: %v", err), Model: c.model})
		}
		if err := finishCLIStream(ctx, cmd, stderrBuf); err != nil {
			emit(ctx, out, ai.Event{Kind: ai.EventError, Error: err.Error(), Model: c.model})
		}
	}()
	return out, nil
}

func buildClaudeCLIArgs(model string, req ai.Request) ([]string, func(), error) {
	args := []string{"-p", "--verbose", "--output-format", "stream-json"}
	cleanup := func() {}
	// claude-cli advertises tool-policy support, but only for allow/deny: the
	// guard still has to reject the policies no transport can express.
	if err := api.RequireToolPolicySupport(api.BackendClaudeCLI, req.Permissions); err != nil {
		return nil, cleanup, err
	}
	if m := claudeCLIModel(model); m != "" {
		args = append(args, "--model", m)
	}
	if req.Prompt.System != "" {
		args = append(args, "--system-prompt", req.Prompt.System)
	}
	if req.Prompt.AppendSystem != "" {
		args = append(args, "--append-system-prompt", req.Prompt.AppendSystem)
	}
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}
	if req.Effort != "" {
		args = append(args, "--effort", string(req.Effort))
	}
	if req.Budget.Cost > 0 {
		args = append(args, "--max-budget-usd", fmt.Sprintf("%g", req.Budget.Cost))
	}
	if mode := cliClaudePermissionMode(req.Permissions.Mode); mode != "" {
		args = append(args, "--permission-mode", mode)
	}
	if allow := req.Permissions.Tools.AllowList(); len(allow) > 0 {
		args = append(args, "--allowedTools", strings.Join(allow, ","))
	}
	if deny := req.Permissions.Tools.DenyList(); len(deny) > 0 {
		args = append(args, "--disallowedTools", strings.Join(deny, ","))
	}
	for _, dir := range req.Memory.Skills {
		if strings.TrimSpace(dir) != "" {
			args = append(args, "--plugin-dir", dir)
		}
	}
	if req.Memory.SkipSkills {
		args = append(args, "--disable-slash-commands")
	}
	if req.Memory.Bare || req.Permissions.HasPreset(api.PresetBare) {
		args = append(args, "--bare")
	}
	if req.Permissions.MCP.Disabled {
		args = append(args, "--mcp-config", `{"mcpServers":{}}`, "--strict-mcp-config")
	}
	if binary, ok := captainBinary(); ok && api.MonitorHooksEnabled(req) {
		settingsPath, remove, err := writeClaudeMonitorSettings(binary)
		if err != nil {
			return nil, cleanup, err
		}
		cleanup = remove
		args = append(args, "--settings", settingsPath)
	}
	schema, err := ai.SchemaJSONForBackend(ai.BackendClaudeCLI, req.Prompt)
	if err != nil {
		return nil, cleanup, fmt.Errorf("claude-cli: cannot derive structured-output schema: %w", err)
	}
	if len(schema) > 0 {
		args = append(args, "--json-schema", string(schema))
	}
	return args, cleanup, nil
}

func claudeCLIModel(model string) string {
	model = strings.TrimSpace(model)
	if model == "claude" {
		return ""
	}
	return ai.NormalizeModelForBackend(ai.BackendClaudeCLI, model)
}

func cliClaudePermissionMode(mode api.PermissionMode) string {
	switch mode {
	case "", api.PermissionDefault:
		return ""
	case api.PermissionAuto:
		return "auto"
	case api.PermissionAcceptEdits:
		return "acceptEdits"
	case api.PermissionBypass:
		return "bypassPermissions"
	case api.PermissionDontAsk:
		return "dontAsk"
	case api.PermissionPlan:
		return "plan"
	default:
		return string(mode)
	}
}

func claudeEntryEvents(entry claude.HistoryEntry, fallbackModel string) []ai.Event {
	model := firstNonEmpty(entry.Message.Model, fallbackModel)
	var out []ai.Event
	for _, block := range entry.Message.Content {
		switch block.Type {
		case claude.ContentTypeText:
			if block.Text != "" && entry.Message.Role == claude.MessageRoleAssistant {
				out = append(out, ai.Event{Kind: ai.EventText, Text: block.Text, SessionID: entry.SessionID, Model: model})
			}
		case claude.ContentTypeThinking:
			if block.Thinking != "" {
				out = append(out, ai.Event{Kind: ai.EventThinking, Text: block.Thinking, SessionID: entry.SessionID, Model: model})
			}
		case claude.ContentTypeToolUse:
			input := map[string]any{}
			if len(block.Input) > 0 {
				_ = json.Unmarshal(block.Input, &input)
			}
			ev := ai.Event{Kind: ai.EventToolUse, Tool: block.Name, Input: input, ToolCallID: block.ID, SessionID: entry.SessionID, Model: model, Raw: entry}
			switch block.Name {
			case "SessionInit":
				ev.Kind = ai.EventSystem
				ev.Tool = "SessionInit"
				if session, _ := input["session_id"].(string); session != "" {
					ev.SessionID = session
				}
			case "Result":
				ev = claudeResultEvent(entry, block, input, model)
			case "ApiError":
				ev.Kind = ai.EventError
				ev.Error = firstString(input, "error", "message", "result")
				ev.Raw = entry
			}
			out = append(out, ev)
		case claude.ContentTypeToolResult:
			ev := ai.Event{Kind: ai.EventToolResult, Text: string(block.Content), ToolCallID: block.ToolUseID, Success: !block.IsError, SessionID: entry.SessionID, Model: model, Raw: entry}
			out = append(out, ev)
		}
	}
	return out
}

func claudeResultEvent(entry claude.HistoryEntry, block claude.ContentBlock, input map[string]any, model string) ai.Event {
	ev := ai.Event{
		Kind:      ai.EventResult,
		Tool:      "Result",
		Success:   !truthy(input["is_error"]),
		SessionID: entry.SessionID,
		Model:     model,
		Input:     input,
		Raw:       entry,
	}
	if result, _ := input["result"].(string); result != "" {
		ev.Text = result
	}
	if ev.SessionID == "" {
		if session, _ := input["session_id"].(string); session != "" {
			ev.SessionID = session
		}
	}
	if errText := firstString(input, "error", "result"); !ev.Success && errText != "" {
		ev.Error = errText
	}
	if cost, ok := number(input["total_cost_usd"]); ok {
		ev.CostUSD = cost
	}
	ev.Usage = claudeResultUsage(entry, input)
	if len(block.Input) > 0 {
		ev.StructuredData = structuredJSONFromResult(input)
	}
	return ev
}

// claudeResultUsage reads a result event's token counts. A stream-json `result`
// line reports the turn's totals at the top level, so the synthetic entry built
// for it carries no message-level usage; the flattened input map is the source
// of truth there, and Message.Usage only for a real assistant message.
func claudeResultUsage(entry claude.HistoryEntry, input map[string]any) *ai.Usage {
	usage := entry.Message.Usage
	if usage == nil {
		raw, ok := input["usage"]
		if !ok {
			return nil
		}
		encoded, err := json.Marshal(raw)
		if err != nil {
			return nil
		}
		var decoded claude.Usage
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return nil
		}
		usage = &decoded
	}
	return &ai.Usage{
		InputTokens:      usage.InputTokens,
		OutputTokens:     usage.OutputTokens,
		CacheReadTokens:  usage.CacheReadInputTokens,
		CacheWriteTokens: usage.CacheCreationInputTokens,
	}
}

func structuredJSONFromResult(input map[string]any) json.RawMessage {
	result, _ := input["result"].(string)
	if strings.TrimSpace(result) == "" {
		return nil
	}
	var js json.RawMessage
	if json.Unmarshal([]byte(result), &js) == nil {
		return js
	}
	return nil
}

func firstString(input map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := input[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true"
	default:
		return false
	}
}

func number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}
