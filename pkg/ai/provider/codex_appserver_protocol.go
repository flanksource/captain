package provider

import (
	"encoding/json"
	"fmt"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/history"
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
	ID               string              `json:"id"`
	Type             string              `json:"type"`
	Text             string              `json:"text"`
	Command          string              `json:"command"`
	Tool             string              `json:"tool"`
	Server           string              `json:"server"`
	CWD              string              `json:"cwd"`
	Status           string              `json:"status"`
	Arguments        json.RawMessage     `json:"arguments"`
	AggregatedOutput *string             `json:"aggregatedOutput"`
	ExitCode         *int                `json:"exitCode"`
	Success          *bool               `json:"success"`
	Result           json.RawMessage     `json:"result"`
	ContentItems     json.RawMessage     `json:"contentItems"`
	Changes          json.RawMessage     `json:"changes"`
	Error            *appServerErrorBody `json:"error"`
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
	// Codex uses OpenAI accounting: inputTokens folds in the cached prefix and
	// outputTokens folds in reasoning. Net both so pricing/totals do not
	// double-count against cachedInputTokens/reasoningOutputTokens.
	t := n.TokenUsage.Total
	usage.InputTokens = ai.NetInputTokens(t.InputTokens, t.CachedInputTokens)
	usage.OutputTokens = ai.NetOutputTokens(t.OutputTokens, t.ReasoningOutputTokens)
	usage.CacheReadTokens = t.CachedInputTokens
	usage.ReasoningTokens = t.ReasoningOutputTokens
}

// --- notification mapping --------------------------------------------------

// mapAppServerNotification maps one notification into an ai.Event. Pure: it
// folds thread/tokenUsage/updated into usage (ok=false) and reads the folded
// usage back out on turn/completed.
type appServerEventContext struct {
	Model      string
	Usage      *ai.Usage
	ToolOutput string
}

func mapAppServerNotification(method string, params json.RawMessage, ctx appServerEventContext) (ai.Event, bool) {
	n := parseAppServerNotif(params)
	switch method {
	case "thread/started":
		sid := n.threadID()
		out := ai.Event{Kind: ai.EventSystem, Tool: "SessionInit", SessionID: sid, Model: ctx.Model}
		out.Raw = codexSessionToolUse(sid, ctx.Model)
		return out, true

	case "item/agentMessage/delta":
		if n.Delta == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventText, Text: n.Delta, SessionID: n.threadID(), Model: ctx.Model}, true

	case "item/reasoning/textDelta", "item/reasoning/summaryTextDelta":
		if n.Delta == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventThinking, Text: n.Delta, SessionID: n.threadID(), Model: ctx.Model}, true

	case "item/commandExecution/outputDelta":
		return ai.Event{}, false

	case "item/started", "item/completed":
		return mapAppServerItem(method, n.Item, n.threadID(), ctx)

	case "thread/tokenUsage/updated":
		n.foldUsage(ctx.Usage)
		return ai.Event{}, false

	case "turn/completed":
		out := ai.Event{Kind: ai.EventResult, Tool: "Result", SessionID: n.threadID(), Model: ctx.Model, Success: true}
		if ctx.Usage != nil && ctx.Usage.TotalTokens() > 0 {
			u := *ctx.Usage
			out.Usage = &u
		}
		out.Raw = codexResultToolUse(out, n.ThreadID)
		return out, true

	case "turn/failed", "error":
		return ai.Event{Kind: ai.EventError, Error: extractCodexErrorText(n.errorText()), SessionID: n.threadID(), Model: ctx.Model}, true
	}
	return ai.Event{}, false
}

// mapAppServerItem dispatches item/started and item/completed on the item type:
// agent messages become text, command/tool/file items become correlated use and
// result events, and reasoning/user/hook items are dropped.
func mapAppServerItem(method string, it *appServerItemBody, sessionID string, ctx appServerEventContext) (ai.Event, bool) {
	if it == nil {
		return ai.Event{}, false
	}
	switch it.Type {
	case "agentMessage", "plan":
		if method != "item/completed" || it.Text == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventText, Text: it.Text, SessionID: sessionID, Model: ctx.Model}, true
	case "reasoning", "userMessage", "hookPrompt", "":
		return ai.Event{}, false
	}
	use := history.NormalizeCodexToolCall(appServerToolCall(it, sessionID, ctx.Model))
	if method == "item/started" {
		out := ai.Event{
			Kind: ai.EventToolUse, Tool: use.Tool, Input: use.Input,
			ToolCallID: use.ToolUseID, SessionID: sessionID, Model: ctx.Model,
		}
		out.Raw = codexToolUse(use, ctx.Model)
		return out, true
	}
	if method != "item/completed" {
		return ai.Event{}, false
	}
	text := appServerToolResultText(it, ctx.ToolOutput)
	success := appServerToolSucceeded(it)
	use.Response = text
	raw := codexToolUse(use, ctx.Model)
	raw.IsError = !success
	return ai.Event{
		Kind: ai.EventToolResult, Text: text, ToolCallID: use.ToolUseID,
		Success: success, SessionID: sessionID, Model: ctx.Model, Raw: raw,
	}, true
}

func appServerToolCall(it *appServerItemBody, sessionID, model string) history.CodexToolCall {
	name := it.Tool
	input := map[string]any{}
	switch it.Type {
	case "commandExecution":
		name = ""
	case "fileChange":
		name = "CodexPatchApply"
		if len(it.Changes) > 0 {
			var changes any
			_ = json.Unmarshal(it.Changes, &changes)
			input["changes"] = changes
		}
	default:
		name = firstNonEmpty(name, it.Type)
	}
	return history.CodexToolCall{
		Name: name, Namespace: it.Server, Arguments: it.Arguments,
		Command: it.Command, Input: input, CWD: it.CWD,
		SessionID: sessionID, ID: it.ID, Model: model,
	}
}

func appServerToolResultText(it *appServerItemBody, buffered string) string {
	if it.AggregatedOutput != nil && *it.AggregatedOutput != "" {
		return *it.AggregatedOutput
	}
	if buffered != "" {
		return buffered
	}
	if it.AggregatedOutput != nil {
		return *it.AggregatedOutput
	}
	if it.Error != nil {
		return firstNonEmpty(it.Error.Message, it.Error.AdditionalDetails)
	}
	for _, raw := range []json.RawMessage{it.Result, it.ContentItems, it.Changes} {
		if len(raw) > 0 && string(raw) != "null" {
			return string(raw)
		}
	}
	return it.Text
}

func appServerToolSucceeded(it *appServerItemBody) bool {
	if it.Success != nil {
		return *it.Success
	}
	if it.ExitCode != nil && *it.ExitCode != 0 {
		return false
	}
	return it.Status == "completed"
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
	if cwd := req.Cwd(); cwd != "" {
		p["cwd"] = cwd
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
// enums via the shared api.CodexSafety mapping (also used by the cmux backend).
// Approvals are auto-accepted (handleApproval), so the policy only affects when
// codex prompts, never whether work proceeds.
func codexSafety(req ai.Request) (sandbox, approval string) {
	s, a := api.CodexSafety(req.Permissions)
	return string(s), string(a)
}

// buildTurnStartParams builds the turn/start params. outputSchema, when
// non-empty, is sent as the turn-scoped `outputSchema` that constrains the final
// assistant message to validated JSON (structured output); the raw JSON Schema
// bytes are embedded inline verbatim.

func buildTurnStartParams(model string, req ai.Request, threadID string, outputSchema json.RawMessage) (map[string]any, error) {
	if err := ai.ValidateAttachmentCompatibility([]api.Model{{Name: model, Backend: api.BackendCodexAgent}}, req.Prompt.Attachments); err != nil {
		return nil, err
	}
	input := make([]map[string]any, 0, len(req.Prompt.Attachments))
	if text := composePrompt(req); text != "" {
		input = append(input, map[string]any{"type": "text", "text": text})
	}
	for i, attachment := range req.Prompt.Attachments {
		content, ok := attachment.PreparedContent()
		if !ok || content.Path == "" {
			return nil, fmt.Errorf("attachment %d (%s) has no prepared local path", i+1, attachment.ID)
		}
		input = append(input, map[string]any{"type": "localImage", "path": content.Path})
	}
	p := map[string]any{
		"threadId": threadID,
		"input":    input,
	}
	if model != "" {
		p["model"] = model
	}
	if req.Effort != "" {
		p["effort"] = string(req.Effort)
	}
	if len(outputSchema) > 0 {
		p["outputSchema"] = outputSchema
	}
	return p, nil
}

func buildResumeParams(req ai.Request) map[string]any {
	p := map[string]any{"threadId": req.SessionID}
	if cwd := req.Cwd(); cwd != "" {
		p["cwd"] = cwd
	}
	return p
}
