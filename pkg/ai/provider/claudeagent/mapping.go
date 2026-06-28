package claudeagent

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/claude"
)

// JSON-RPC notification methods emitted by agent.ts.
const (
	notifySessionInit  = "session/init"
	notifyMessageText  = "message/text"
	notifyMessageThink = "message/thinking"
	notifyToolUse      = "message/tool_use"
	notifyToolResult   = "message/tool_result"
	notifyTurnDone     = "turn/completed"
	notifyTurnError    = "turn/error"
)

// claudeSource tags the synthetic claude.ToolUse rows stashed on Event.Raw so
// the shared pkg/cli renderer treats live claude-agent events exactly like
// `captain history` output.
const claudeSource = "claude"

// mapNotification translates a single agent.ts JSON-RPC notification into an
// ai.Event. It is pure (no process / IO) so it can be unit-tested directly. The
// bool is false for notifications that carry no renderable event (unknown
// methods, empty text). For tool_use, result and session rows it stashes a
// claude.ToolUse on Event.Raw with Source "claude", mirroring the codex_cli
// stash pattern.
func mapNotification(method string, params json.RawMessage, model string) (ai.Event, bool) {
	switch method {
	case notifySessionInit:
		var p struct {
			SessionID string   `json:"session_id"`
			Model     string   `json:"model"`
			Tools     []string `json:"tools"`
		}
		_ = json.Unmarshal(params, &p)
		m := firstNonEmpty(p.Model, model)
		ev := ai.Event{
			Kind:      ai.EventSystem,
			Tool:      "SessionInit",
			SessionID: p.SessionID,
			Model:     m,
		}
		if len(p.Tools) > 0 {
			ev.Input = map[string]any{"session_id": p.SessionID, "model": m, "tools": toAnySlice(p.Tools)}
		}
		ev.Raw = sessionToolUse(p.SessionID, m)
		return ev, true

	case notifyMessageText:
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Text == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventText, Text: p.Text, Model: model}, true

	case notifyMessageThink:
		var p struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(params, &p)
		if p.Text == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventThinking, Text: p.Text, Model: model}, true

	case notifyToolUse:
		var p struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
			ID    string         `json:"id"`
		}
		_ = json.Unmarshal(params, &p)
		ev := ai.Event{
			Kind:       ai.EventToolUse,
			Tool:       p.Tool,
			Input:      p.Input,
			ToolCallID: p.ID,
			Model:      model,
		}
		ev.Raw = toolUse(p.Tool, p.Input, p.ID, model)
		return ev, true

	case notifyToolResult:
		var p struct {
			ID      string `json:"id"`
			Content string `json:"content"`
			IsError bool   `json:"is_error"`
		}
		_ = json.Unmarshal(params, &p)
		ev := ai.Event{
			Kind:       ai.EventToolResult,
			Text:       p.Content,
			ToolCallID: p.ID,
			Success:    !p.IsError,
			Model:      model,
		}
		ev.Raw = toolResultUse(p.ID, p.Content, p.IsError, model)
		return ev, true

	case notifyTurnDone:
		var p struct {
			Success    bool            `json:"success"`
			SessionID  string          `json:"session_id"`
			CostUSD    float64         `json:"cost_usd"`
			Usage      json.RawMessage `json:"usage"`
			NumTurns   int             `json:"num_turns"`
			ResultText string          `json:"result_text"`
		}
		_ = json.Unmarshal(params, &p)
		ev := ai.Event{
			Kind:      ai.EventResult,
			Tool:      "Result",
			Success:   p.Success,
			CostUSD:   p.CostUSD,
			SessionID: p.SessionID,
			Usage:     decodeUsage(p.Usage),
			Model:     model,
		}
		input := map[string]any{"is_error": !p.Success}
		if p.CostUSD > 0 {
			input["total_cost_usd"] = p.CostUSD
		}
		if p.SessionID != "" {
			input["session_id"] = p.SessionID
		}
		if p.NumTurns > 0 {
			input["num_turns"] = p.NumTurns
		}
		if p.ResultText != "" {
			input["result"] = p.ResultText
		}
		ev.Input = input
		ev.Raw = resultToolUse(ev, p.SessionID)
		return ev, true

	case notifyTurnError:
		var p struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(params, &p)
		return ai.Event{Kind: ai.EventError, Error: p.Message, Model: model}, true
	}

	return ai.Event{}, false
}

// toolUse builds the claude.ToolUse stand-in for a live tool_use row.
func toolUse(name string, input map[string]any, id, model string) claude.ToolUse {
	return claude.ToolUse{
		Tool:      name,
		Input:     input,
		ToolUseID: id,
		Source:    claudeSource,
		Model:     model,
	}
}

// toolResultUse builds the claude.ToolUse stand-in for a tool result row: the
// output (Response) and error state keyed by the originating call id, so the
// shared renderer pairs it with the call.
func toolResultUse(id, content string, isError bool, model string) claude.ToolUse {
	return claude.ToolUse{
		ToolUseID: id,
		Response:  content,
		IsError:   isError,
		Source:    claudeSource,
		Model:     model,
	}
}

// resultToolUse mirrors the synthetic Result row pkg/cli renders for claude so
// the "result" footer shows cost, usage and error state consistently.
func resultToolUse(ev ai.Event, sessionID string) claude.ToolUse {
	input := map[string]any{}
	for k, v := range ev.Input {
		input[k] = v
	}
	tu := claude.ToolUse{
		Tool:      "Result",
		Input:     input,
		SessionID: sessionID,
		Source:    claudeSource,
		Model:     ev.Model,
	}
	if ev.Usage != nil {
		tu.InputTokens = ev.Usage.InputTokens
		tu.OutputTokens = ev.Usage.OutputTokens
	}
	if !ev.Success {
		tu.IsError = true
		if ev.Error != "" {
			if _, ok := tu.Input["result"]; !ok {
				tu.Input["result"] = ev.Error
			}
		}
	}
	return tu
}

// sessionToolUse builds the synthetic SessionInit row that triggers the shared
// renderer's session-start banner.
func sessionToolUse(sessionID, model string) claude.ToolUse {
	return claude.ToolUse{
		Tool:      "SessionInit",
		SessionID: sessionID,
		Source:    claudeSource,
		Model:     model,
	}
}

// decodeUsage parses the SDK usage object into ai.Usage, returning nil when no
// token fields are present.
func decodeUsage(raw json.RawMessage) *ai.Usage {
	if len(raw) == 0 {
		return nil
	}
	var m struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
		CacheRead    int `json:"cache_read_input_tokens"`
		CacheCreate  int `json:"cache_creation_input_tokens"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	if m.InputTokens == 0 && m.OutputTokens == 0 && m.CacheRead == 0 && m.CacheCreate == 0 {
		return nil
	}
	return &ai.Usage{
		InputTokens:      m.InputTokens,
		OutputTokens:     m.OutputTokens,
		CacheReadTokens:  m.CacheRead,
		CacheWriteTokens: m.CacheCreate,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func toAnySlice(in []string) []any {
	out := make([]any, len(in))
	for i, v := range in {
		out[i] = v
	}
	return out
}
