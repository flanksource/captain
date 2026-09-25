package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	CallID           string              `json:"call_id"`
	Output           json.RawMessage     `json:"output"`
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
	ID     string              `json:"id"`
	Status string              `json:"status"`
	Error  *appServerErrorBody `json:"error"`
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
// folds thread/tokenUsage/updated into usage (ok=false), records that the update
// existed even when all buckets are zero, and emits it on turn/completed.
type appServerEventContext struct {
	Model        string
	Usage        *ai.Usage
	UsagePresent *bool
	ToolOutput   string
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
		if ctx.UsagePresent != nil && n.TokenUsage != nil {
			*ctx.UsagePresent = true
		}
		n.foldUsage(ctx.Usage)
		return ai.Event{}, false

	case "turn/completed":
		if n.Turn != nil {
			switch n.Turn.Status {
			case "interrupted":
				return ai.Event{}, false
			case "failed":
				message := "codex turn failed"
				if n.Turn.Error != nil {
					message = firstNonEmpty(n.Turn.Error.Message, n.Turn.Error.AdditionalDetails, message)
				}
				return ai.Event{Kind: ai.EventError, Error: extractCodexErrorText(message), SessionID: n.threadID(), Model: ctx.Model}, true
			}
		}
		out := ai.Event{Kind: ai.EventResult, Tool: "Result", SessionID: n.threadID(), Model: ctx.Model, Success: true}
		if ctx.Usage != nil && ctx.UsagePresent != nil && *ctx.UsagePresent {
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

func appServerRawCommandOutput(params json.RawMessage) (string, string, bool) {
	it := parseAppServerNotif(params).Item
	if it == nil || it.Type != "function_call_output" || it.CallID == "" || len(it.Output) == 0 {
		return "", "", false
	}
	text := history.CodexCommandOutputText(history.CodexOutputText(it.Output))
	return it.CallID, text, true
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

func appServerAgentMessageRemainder(params json.RawMessage, streamed map[string]string) (string, bool, error) {
	it := parseAppServerNotif(params).Item
	if it == nil || it.Type != "agentMessage" {
		return "", false, nil
	}
	prefix, ok := streamed[it.ID]
	if !ok {
		return "", false, nil
	}
	if !strings.HasPrefix(it.Text, prefix) {
		return "", true, fmt.Errorf("codex app-server completed agent message %q does not extend its streamed text", it.ID)
	}
	return strings.TrimPrefix(it.Text, prefix), true, nil
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
// the versioned thread/start schema. Raw response items provide the authoritative
// command output when Codex's normal command item misses an early output chunk.
func buildThreadStartParams(model string, req ai.Request, callerTools *api.CallerToolEndpoint) (map[string]any, error) {
	p := map[string]any{"experimentalRawEvents": true}
	if cwd := req.Cwd(); cwd != "" {
		p["cwd"] = cwd
	}
	if model != "" {
		p["model"] = model
	}
	translation, roots, err := codexAppServerSafety(req)
	if err != nil {
		return nil, err
	}
	applyCodexThreadSafety(p, translation, roots)
	if req.Memory.SkipMemory || req.Memory.Bare || req.Permissions.HasPreset(api.PresetBare) {
		p["ephemeral"] = true
	}
	if config := codexThreadConfig(req, callerTools, translation.WorkspaceWrite); config != nil {
		p["config"] = config
	}
	return p, nil
}

// buildTurnStartParams builds the turn/start params. outputSchema, when
// non-empty, is sent as the turn-scoped `outputSchema` that constrains the final
// assistant message to validated JSON (structured output); the raw JSON Schema
// bytes are embedded inline verbatim.
func buildTurnStartParams(model string, req ai.Request, threadID string, outputSchema json.RawMessage) (map[string]any, error) {
	if err := ai.ValidateAttachmentCompatibility([]api.Model{{Name: model, Provider: api.OpenAI, Mode: api.ModeAgent}}, req.Prompt.Attachments); err != nil {
		return nil, err
	}
	if err := api.RequireToolPolicySupport(api.OpenAI, api.ModeAgent, req.Permissions); err != nil {
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
	mode := "default"
	if req.Permissions.Mode == api.PermissionPlan {
		mode = "plan"
	}
	var effort any
	if req.Effort != "" {
		effort = string(req.Effort)
	}
	p["collaborationMode"] = map[string]any{
		"mode": mode,
		"settings": map[string]any{
			"model": model, "reasoning_effort": effort, "developer_instructions": nil,
		},
	}
	translation, roots, err := codexAppServerSafety(req)
	if err != nil {
		return nil, err
	}
	if cwd := req.Cwd(); cwd != "" {
		p["cwd"] = cwd
	}
	if translation.Approval != "" {
		p["approvalPolicy"] = string(translation.Approval)
	}
	if translation.ApprovalsReviewer != "" {
		p["approvalsReviewer"] = string(translation.ApprovalsReviewer)
	}
	if len(roots) > 0 {
		p["runtimeWorkspaceRoots"] = roots
	}
	if translation.Sandbox != "" {
		p["sandboxPolicy"] = codexTurnSandboxPolicy(translation)
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

func buildResumeParams(req ai.Request, callerTools *api.CallerToolEndpoint) (map[string]any, error) {
	// Unlike thread/start, Codex's versioned thread/resume schema does not expose
	// experimentalRawEvents. A cold-resumed thread therefore uses the ordinary
	// command-output fallback until Codex adds that protocol capability.
	p := map[string]any{"threadId": req.SessionID}
	if cwd := req.Cwd(); cwd != "" {
		p["cwd"] = cwd
	}
	translation, roots, err := codexAppServerSafety(req)
	if err != nil {
		return nil, err
	}
	applyCodexThreadSafety(p, translation, roots)
	if config := codexThreadConfig(req, callerTools, translation.WorkspaceWrite); config != nil {
		p["config"] = config
	}
	return p, nil
}

func codexAppServerSafety(req ai.Request) (api.CodexSandboxTranslation, []string, error) {
	translation, err := translateCodexSandbox(api.RuntimeOf(api.OpenAI, api.ModeAgent), req)
	if err != nil {
		return api.CodexSandboxTranslation{}, nil, err
	}
	roots, err := codexRuntimeWorkspaceRoots(req)
	if err != nil {
		return api.CodexSandboxTranslation{}, nil, err
	}
	return translation, roots, nil
}

func codexRuntimeWorkspaceRoots(req ai.Request) ([]string, error) {
	directories := req.Permissions.CleanDirectories()
	if len(directories) == 0 {
		return nil, nil
	}
	cwd := req.Cwd()
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve Codex runtime workspace cwd: %w", err)
		}
	}
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return nil, fmt.Errorf("resolve Codex runtime workspace cwd %q: %w", cwd, err)
	}
	roots := []string{filepath.Clean(absCwd)}
	seen := map[string]struct{}{roots[0]: {}}
	for _, directory := range directories {
		if !filepath.IsAbs(directory) {
			directory = filepath.Join(absCwd, directory)
		}
		directory = filepath.Clean(directory)
		if _, exists := seen[directory]; exists {
			continue
		}
		seen[directory] = struct{}{}
		roots = append(roots, directory)
	}
	return roots, nil
}

func applyCodexThreadSafety(params map[string]any, translation api.CodexSandboxTranslation, roots []string) {
	if translation.Sandbox != "" {
		params["sandbox"] = string(translation.Sandbox)
	}
	if translation.Approval != "" {
		params["approvalPolicy"] = string(translation.Approval)
	}
	if translation.ApprovalsReviewer != "" {
		params["approvalsReviewer"] = string(translation.ApprovalsReviewer)
	}
	if len(roots) > 0 {
		params["runtimeWorkspaceRoots"] = roots
	}
}

func codexTurnSandboxPolicy(translation api.CodexSandboxTranslation) map[string]any {
	switch translation.Sandbox {
	case api.CodexSandboxDangerFull:
		return map[string]any{"type": "dangerFullAccess"}
	case api.CodexSandboxReadOnly:
		policy := map[string]any{"type": "readOnly"}
		if networkAccess, ok := translation.WorkspaceWrite["network_access"]; ok {
			policy["networkAccess"] = networkAccess
		}
		return policy
	case api.CodexSandboxWorkspaceWrite:
		policy := map[string]any{"type": "workspaceWrite"}
		for source, target := range map[string]string{
			"writable_roots": "writableRoots", "exclude_slash_tmp": "excludeSlashTmp",
			"exclude_tmpdir_env_var": "excludeTmpdirEnvVar", "network_access": "networkAccess",
		} {
			if value, ok := translation.WorkspaceWrite[source]; ok {
				policy[target] = value
			}
		}
		return policy
	default:
		panic(fmt.Sprintf("unsupported validated Codex sandbox %q", translation.Sandbox))
	}
}

func codexThreadConfig(req ai.Request, callerTools *api.CallerToolEndpoint, workspaceWrite map[string]any) map[string]any {
	config := map[string]any{}
	// mcp.disabled replaces every ambient server with captain's own caller-tool
	// server, or with nothing when there are no caller tools.
	servers := map[string]any{}
	if callerTools != nil {
		servers[callerTools.Name] = map[string]any{
			"url": callerTools.URL, "http_headers": cloneStringMap(callerTools.Headers),
			"required": true, "enabled": true, "default_tools_approval_mode": "approve",
		}
	}
	if req.Permissions.MCP.Disabled || callerTools != nil {
		config["mcp_servers"] = servers
	}
	if len(workspaceWrite) > 0 {
		config["sandbox_workspace_write"] = workspaceWrite
	}
	if len(config) == 0 {
		return nil
	}
	return config
}
