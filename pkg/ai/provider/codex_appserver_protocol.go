package provider

import (
	"encoding/json"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

// This file holds the PURE (no I/O, no process/goroutine state) half of the
// codex app-server provider: the notification→ai.Event mapping, the JSON parse
// structs, and the request-param builders. It is split out from the
// supervised-process lifecycle in codex_appserver.go so each file stays focused
// and under the repo's per-file line limit; both halves share package provider.

// --- parse structs ---------------------------------------------------------

// appServerNotif is the permissive union of every notification/response field
// captain reads. Parsing one struct keeps the mapping tolerant of schema bumps
// across codex versions (missing fields degrade to zero values, never panics).
type appServerNotif struct {
	Delta       string               `json:"delta"`
	ItemID      string               `json:"itemId"`
	ThreadID    string               `json:"threadId"`
	ThreadSnake string               `json:"thread_id"`
	WillRetry   bool                 `json:"willRetry"`
	Message     string               `json:"message"`
	Thread      *appServerThread     `json:"thread"`
	Item        *appServerItemBody   `json:"item"`
	Error       *appServerErrorBody  `json:"error"`
	Turn        *appServerRef        `json:"turn"`
	TokenUsage  *appServerTokenUsage `json:"tokenUsage"`
}

type appServerThread struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
}

type appServerItemBody struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Text    string `json:"text"`
	Command string `json:"command"`
	Tool    string `json:"tool"`
}

type appServerErrorBody struct {
	Message           string `json:"message"`
	AdditionalDetails string `json:"additionalDetails"`
}

type appServerRef struct {
	ID string `json:"id"`
}

type appServerTokenUsage struct {
	Total struct {
		InputTokens           int `json:"inputTokens"`
		OutputTokens          int `json:"outputTokens"`
		CachedInputTokens     int `json:"cachedInputTokens"`
		ReasoningOutputTokens int `json:"reasoningOutputTokens"`
	} `json:"total"`
}

func parseAppServerNotif(raw json.RawMessage) appServerNotif {
	var n appServerNotif
	_ = json.Unmarshal(raw, &n)
	return n
}

// threadID reads the thread id from a notification or a thread/start|resume
// response, tolerating both the nested `thread` object and flat id fields.
func (n appServerNotif) threadID() string {
	if n.Thread != nil {
		if s := firstNonEmpty(n.Thread.ID, n.Thread.SessionID); s != "" {
			return s
		}
	}
	return firstNonEmpty(n.ThreadID, n.ThreadSnake)
}

func (n appServerNotif) errorText() string {
	if n.Error != nil {
		if s := firstNonEmpty(n.Error.Message, n.Error.AdditionalDetails); s != "" {
			return s
		}
	}
	return n.Message
}

func (n appServerNotif) foldUsage(usage *ai.Usage) {
	if usage == nil || n.TokenUsage == nil {
		return
	}
	t := n.TokenUsage.Total
	usage.InputTokens = t.InputTokens
	usage.OutputTokens = t.OutputTokens
	usage.CacheReadTokens = t.CachedInputTokens
	usage.ReasoningTokens = t.ReasoningOutputTokens
}

// --- notification mapping --------------------------------------------------

// mapAppServerNotification maps one notification into an ai.Event. Pure: it
// folds thread/tokenUsage/updated into usage (ok=false) and reads the folded
// usage back out on turn/completed.
func mapAppServerNotification(method string, params json.RawMessage, model string, usage *ai.Usage) (ai.Event, bool) {
	n := parseAppServerNotif(params)
	switch method {
	case "thread/started":
		sid := n.threadID()
		out := ai.Event{Kind: ai.EventSystem, Tool: "SessionInit", SessionID: sid, Model: model}
		out.Raw = codexSessionToolUse(sid, model)
		return out, true

	case "item/agentMessage/delta":
		if n.Delta == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventText, Text: n.Delta, Model: model}, true

	case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
		if n.Delta == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventThinking, Text: n.Delta, Model: model}, true

	case "item/commandExecution/outputDelta":
		if n.Delta == "" {
			return ai.Event{}, false
		}
		input := map[string]any{"delta": n.Delta}
		out := ai.Event{Kind: ai.EventToolUse, Tool: "command", Input: input, Model: model}
		out.Raw = codexToolUse("command", input, n.ItemID, model)
		return out, true

	case "item/started", "item/completed":
		return mapAppServerItem(n.Item, model)

	case "thread/tokenUsage/updated":
		n.foldUsage(usage)
		return ai.Event{}, false

	case "turn/completed":
		out := ai.Event{Kind: ai.EventResult, Tool: "Result", Model: model, Success: true}
		if usage != nil && usage.TotalTokens() > 0 {
			u := *usage
			out.Usage = &u
		}
		out.Raw = codexResultToolUse(out, n.ThreadID)
		return out, true

	case "turn/failed", "error":
		return ai.Event{Kind: ai.EventError, Error: extractCodexErrorText(n.errorText()), Model: model}, true
	}
	return ai.Event{}, false
}

// mapAppServerItem dispatches item/started and item/completed on the item type:
// agent messages become text, command/tool/file items become tool-use events,
// and reasoning/user/hook items are dropped (reasoning text arrives via deltas).
func mapAppServerItem(it *appServerItemBody, model string) (ai.Event, bool) {
	if it == nil {
		return ai.Event{}, false
	}
	switch it.Type {
	case "agentMessage", "plan":
		if it.Text == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventText, Text: it.Text, Model: model}, true
	case "reasoning", "userMessage", "hookPrompt", "":
		return ai.Event{}, false
	default:
		name := firstNonEmpty(it.Tool, it.Command, it.Type)
		input := map[string]any{}
		if it.Command != "" {
			input["command"] = it.Command
		}
		if it.Text != "" {
			input["text"] = it.Text
		}
		out := ai.Event{Kind: ai.EventToolUse, Tool: name, Input: input, Model: model}
		out.Raw = codexToolUse(name, input, it.ID, model)
		return out, true
	}
}

// appServerErrorIsFatal reports whether an error/turn-failure notification ends
// the turn. A retryable error (willRetry=true) is surfaced but not terminal.
func appServerErrorIsFatal(method string, params json.RawMessage) bool {
	if method == "turn/failed" {
		return true
	}
	if method != "error" {
		return false
	}
	return !parseAppServerNotif(params).WillRetry
}

// appServerStreamedAgentMessage reports whether an item/completed notification is
// an agent message whose text was already streamed via item/agentMessage/delta.
func appServerStreamedAgentMessage(params json.RawMessage, streamed map[string]bool) bool {
	it := parseAppServerNotif(params).Item
	return it != nil && it.Type == "agentMessage" && streamed[it.ID]
}

// --- request params --------------------------------------------------------

func composePrompt(req ai.Request) string {
	prompt := req.Prompt.User
	if req.Prompt.System != "" {
		prompt = req.Prompt.System + "\n\n" + prompt
	}
	if req.Prompt.AppendSystem != "" {
		prompt = prompt + "\n\n" + req.Prompt.AppendSystem
	}
	return prompt
}

// buildThreadStartParams translates the provider-agnostic safety knobs into
// thread/start params. The CLI-only ignore-user-config / ignore-rules flags
// (req.Memory.SkipUser/SkipProject/SkipHooks) have no first-class equivalent in
// the versioned thread/start schema, so only ephemeral + an empty mcp_servers
// override (the knobs the protocol exposes) are emitted.
func buildThreadStartParams(model string, req ai.Request) map[string]any {
	p := map[string]any{}
	if req.Context.Dir != "" {
		p["cwd"] = req.Context.Dir
	}
	if model != "" {
		p["model"] = model
	}
	sandbox, approval := codexSafety(req)
	p["sandbox"], p["approvalPolicy"] = sandbox, approval
	if req.Memory.SkipMemory || req.Memory.Bare {
		p["ephemeral"] = true
	}
	if req.Permissions.MCP.Disabled {
		p["config"] = map[string]any{"mcp_servers": map[string]any{}}
	}
	return p
}

// codexSafety maps edit/permission knobs onto the SandboxMode + AskForApproval
// enums. Approvals are auto-accepted (handleApproval), so the policy only affects
// when codex prompts, never whether work proceeds.
func codexSafety(req ai.Request) (sandbox, approval string) {
	switch {
	case req.Permissions.Mode == api.PermissionBypass:
		return "danger-full-access", "never"
	case req.Permissions.HasPreset(api.PresetEdit) && req.Permissions.Mode == "":
		return "workspace-write", "on-request"
	default:
		return "read-only", "on-request"
	}
}

func buildTurnStartParams(model string, req ai.Request, threadID string) map[string]any {
	p := map[string]any{
		"threadId": threadID,
		"input":    []map[string]any{{"type": "text", "text": composePrompt(req)}},
	}
	if model != "" {
		p["model"] = model
	}
	if req.Effort != "" {
		p["effort"] = string(req.Effort)
	}
	return p
}

func buildResumeParams(req ai.Request) map[string]any {
	p := map[string]any{"threadId": req.SessionID}
	if req.Context.Dir != "" {
		p["cwd"] = req.Context.Dir
	}
	return p
}
