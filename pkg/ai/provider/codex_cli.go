package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	history "github.com/flanksource/captain/pkg/ai/history"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/claude"
)

var codexCLICommand = "codex"

type CodexCLI struct {
	model   string
	apiURL  string
	sandbox *api.SandboxConfig
}

func NewCodexCLI(cfg ai.Config) *CodexCLI {
	model := cfg.Model.Name
	if strings.TrimSpace(model) == "" {
		model = CodexCLIDefaultModel
	}
	return &CodexCLI{
		model:   ai.NormalizeModelForBackend(ai.BackendCodexCLI, model),
		apiURL:  strings.TrimSpace(cfg.APIURL),
		sandbox: cfg.ResolvedSandbox(),
	}
}

func (c *CodexCLI) GetModel() string       { return c.model }
func (c *CodexCLI) GetBackend() ai.Backend { return ai.BackendCodexCLI }

func (c *CodexCLI) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()
	events, err := c.ExecuteStream(ctx, req)
	if err != nil {
		return nil, err
	}
	resp, err := CoalesceStreamForBackend(ctx, ai.BackendCodexCLI, c.model, events, start)
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

func (c *CodexCLI) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	args, cleanup, err := buildCodexCLIArgs(codexCLIConfig{Model: c.model, APIURL: c.apiURL}, req)
	if err != nil {
		return nil, err
	}
	cmd, stdout, stderrBuf, closeSandbox, err := startCLIStream(ctx, codexCLICommand, args, []byte(composePrompt(req)), &req, c.sandbox)
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
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
		state := codexCLIState{model: c.model, cwd: req.Cwd(), pending: map[string]history.CodexEvent{}}
		for scanner.Scan() {
			for _, ev := range state.mapLine(scanner.Bytes()) {
				emit(ctx, out, ev)
			}
		}
		if err := scanner.Err(); err != nil {
			emit(ctx, out, ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("codex exec --json: %v", err), Model: c.model})
		}
		if err := finishCLIStream(ctx, cmd, stderrBuf); err != nil {
			emit(ctx, out, ai.Event{Kind: ai.EventError, Error: err.Error(), Model: c.model})
		}
	}()
	return out, nil
}

// codexCLIConfig is the part of the provider config that shapes argv.
type codexCLIConfig struct {
	Model  string
	APIURL string
}

// codexOverrideProvider names the ad-hoc model provider captain declares when a
// base URL override is in play. codex ignores OPENAI_BASE_URL once an account
// credential is stored, so redirection has to go through model_providers.
const codexOverrideProvider = "captain"

// codexProviderOverride renders the -c flags that point codex at baseURL. The
// URL is passed through verbatim: codex appends /responses to it, so the caller
// owns the /v1 suffix exactly as they would for OPENAI_BASE_URL.
func codexProviderOverride(baseURL string) []string {
	key := "model_providers." + codexOverrideProvider
	return []string{
		"-c", "model_provider=" + codexOverrideProvider,
		"-c", key + ".name=" + codexOverrideProvider,
		"-c", key + ".base_url=" + baseURL,
		"-c", key + ".env_key=OPENAI_API_KEY",
		"-c", key + ".wire_api=responses",
	}
}

func buildCodexCLIArgs(cfg codexCLIConfig, req ai.Request) ([]string, func(), error) {
	model := cfg.Model
	args := []string{"exec", "--json"}
	cleanup := func() {}
	if err := ai.ValidateAttachmentCompatibility([]api.Model{{Name: model, Backend: api.BackendCodexCLI}}, req.Prompt.Attachments); err != nil {
		return nil, cleanup, err
	}
	if cfg.APIURL != "" {
		args = append(args, codexProviderOverride(cfg.APIURL)...)
	}
	for i, attachment := range req.Prompt.Attachments {
		content, ok := attachment.PreparedContent()
		if !ok || content.Path == "" {
			return nil, cleanup, fmt.Errorf("attachment %d (%s) has no prepared local path", i+1, attachment.ID)
		}
		args = append(args, "--image", content.Path)
	}
	if req.Effort != api.EffortNone {
		args = append(args, "-c", fmt.Sprintf("model_reasoning_effort=%q", req.Effort))
	}
	if m := strings.TrimSpace(model); m != "" && m != "codex" {
		args = append(args, "-m", m)
	}
	if cwd := req.Cwd(); cwd != "" {
		args = append(args, "-C", cwd)
	}
	sandbox, _ := codexSafety(req)
	if sandbox != "" {
		args = append(args, "--sandbox", sandbox)
	}
	if req.Memory.SkipMemory || req.Memory.Bare || req.Permissions.HasPreset(api.PresetBare) {
		args = append(args, "--ephemeral")
	}
	if req.Memory.SkipUser {
		args = append(args, "--ignore-user-config")
	}
	if req.Memory.SkipProject || req.Memory.SkipHooks {
		args = append(args, "--ignore-rules")
	}
	if binary, ok := captainBinary(); ok && api.MonitorHooksEnabled(req) {
		notify, err := codexNotifyOverride(binary)
		if err != nil {
			return nil, cleanup, err
		}
		args = append(args, "-c", notify)
	}
	schema, err := ai.SchemaJSONForBackend(ai.BackendCodexCLI, req.Prompt)
	if err != nil {
		return nil, cleanup, fmt.Errorf("codex-cli: cannot derive structured-output schema: %w", err)
	}
	if len(schema) > 0 {
		path, err := writeTempSchema(schema)
		if err != nil {
			return nil, cleanup, err
		}
		cleanup = func() { _ = os.Remove(path) }
		args = append(args, "--output-schema", path)
	}
	if req.SessionID != "" {
		args = append(args, "resume", req.SessionID)
	}
	return args, cleanup, nil
}

func writeTempSchema(schema json.RawMessage) (string, error) {
	f, err := os.CreateTemp("", "captain-codex-schema-*.json")
	if err != nil {
		return "", fmt.Errorf("codex-cli: create schema file: %w", err)
	}
	path := f.Name()
	if _, err := f.Write(schema); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("codex-cli: write schema file %s: %w", filepath.Base(path), err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("codex-cli: close schema file %s: %w", filepath.Base(path), err)
	}
	return path, nil
}

type codexCLIState struct {
	model     string
	cwd       string
	sessionID string
	pending   map[string]history.CodexEvent
	usage     ai.Usage
	// sawTokenCount records whether a token_count event supplied the authoritative
	// (cache/reasoning-aware) usage, so turn.completed's coarser per-turn counts
	// are used only as a fallback.
	sawTokenCount bool
}

func (s *codexCLIState) mapLine(line []byte) []ai.Event {
	var event history.CodexEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return []ai.Event{{Kind: ai.EventError, Error: fmt.Sprintf("codex json parse: %v", err), Model: s.model}}
	}
	switch event.Type {
	case "thread.started":
		if event.ThreadID != "" {
			s.sessionID = event.ThreadID
		}
		ev := ai.Event{Kind: ai.EventSystem, Tool: "SessionInit", SessionID: s.sessionID, Model: s.model}
		ev.Raw = codexSessionToolUse(s.sessionID, s.model)
		return []ai.Event{ev}
	case "item.completed":
		return s.mapItemCompleted(event)
	case "response_item":
		return s.mapResponseItem(event)
	case "event_msg":
		return s.mapEventMsg(event)
	case "turn.failed", "error":
		msg := extractCodexErrorText(codexErrorMessage(event))
		ev := ai.Event{Kind: ai.EventError, Error: msg, Model: s.model}
		ev.Raw = codexToolUse(history.NormalizeCodexToolCall(history.CodexToolCall{
			Name: "ApiError", Input: map[string]any{"error": msg}, SessionID: s.sessionID,
		}), s.model)
		return []ai.Event{ev}
	case "turn.completed":
		// turn.completed's per-turn usage lacks cache/reasoning detail; prefer the
		// token_count totals when we have them and fall back to these coarse counts
		// only otherwise (they cannot overlap cache, so no netting is needed).
		if event.Usage != nil && !s.sawTokenCount {
			s.usage.InputTokens = event.Usage.InputTokens
			s.usage.OutputTokens = event.Usage.OutputTokens
		}
		usage := s.usage
		ev := ai.Event{Kind: ai.EventResult, Tool: "Result", Success: true, SessionID: s.sessionID, Model: s.model, Usage: &usage}
		ev.Raw = codexResultToolUse(ev, s.sessionID)
		return []ai.Event{ev}
	}
	return nil
}

func (s *codexCLIState) mapResponseItem(event history.CodexEvent) []ai.Event {
	switch event.Payload.Type {
	case "function_call":
		s.pending[event.Payload.CallID] = event
		return nil
	case "function_call_output":
		call, ok := s.pending[event.Payload.CallID]
		if !ok {
			return nil
		}
		delete(s.pending, event.Payload.CallID)
		return s.codexToolEvents([]claude.ToolUse{s.buildFunctionToolUse(call, event)})
	case "reasoning":
		text := codexReasoningText(event)
		if text == "" {
			return nil
		}
		return []ai.Event{{Kind: ai.EventThinking, Text: text, SessionID: s.sessionID, Model: s.model}}
	case "message":
		text := codexMessageText(event)
		if text == "" {
			return nil
		}
		return []ai.Event{{Kind: ai.EventText, Text: text, SessionID: s.sessionID, Model: s.model}}
	}
	return nil
}

func (s *codexCLIState) mapEventMsg(event history.CodexEvent) []ai.Event {
	if event.Payload.Type == "token_count" && event.Payload.Info != nil {
		// Codex reports cumulative totals with inputTokens inclusive of the cached
		// prefix and outputTokens inclusive of reasoning; net both to captain's
		// disjoint buckets so pricing does not double-count (findings B1/B2).
		tu := event.Payload.Info.TotalTokenUsage
		s.usage.InputTokens = ai.NetInputTokens(tu.InputTokens, tu.CachedInputTokens)
		s.usage.OutputTokens = ai.NetOutputTokens(tu.OutputTokens, tu.ReasoningOutputTokens)
		s.usage.CacheReadTokens = tu.CachedInputTokens
		s.usage.ReasoningTokens = tu.ReasoningOutputTokens
		s.sawTokenCount = true
		return nil
	}
	switch event.Payload.Type {
	case "agent_reasoning":
		if event.Payload.Text != "" {
			return []ai.Event{{Kind: ai.EventThinking, Text: event.Payload.Text, SessionID: s.sessionID, Model: s.model}}
		}
	case "agent_message":
		if event.Payload.Message != "" {
			return []ai.Event{{Kind: ai.EventText, Text: event.Payload.Message, SessionID: s.sessionID, Model: s.model}}
		}
	}
	return nil
}

func (s *codexCLIState) mapItemCompleted(event history.CodexEvent) []ai.Event {
	if event.Item == nil {
		return nil
	}
	text := event.Item.Text
	if text == "" {
		for _, content := range event.Item.Content {
			if content.Type == "output_text" && content.Text != "" {
				text += content.Text
			}
		}
	}
	if text != "" {
		return []ai.Event{{Kind: ai.EventText, Text: text, SessionID: s.sessionID, Model: s.model}}
	}
	name := firstNonEmpty(event.Item.Name, event.Item.Type)
	if name == "" {
		return nil
	}
	tu := codexToolUse(history.NormalizeCodexToolCall(history.CodexToolCall{
		Name: name, ID: event.Item.Name, SessionID: s.sessionID,
	}), s.model)
	return s.codexToolEvents([]claude.ToolUse{tu})
}

func (s *codexCLIState) codexToolEvents(toolUses []claude.ToolUse) []ai.Event {
	out := make([]ai.Event, 0, len(toolUses))
	for _, tu := range toolUses {
		ev := ai.Event{
			Kind:       ai.EventToolUse,
			Tool:       tu.Tool,
			Input:      tu.Input,
			ToolCallID: tu.ToolUseID,
			SessionID:  firstNonEmpty(tu.SessionID, s.sessionID),
			Model:      firstNonEmpty(tu.Model, s.model),
			Raw:        tu,
		}
		if tu.Tool == "Assistant" {
			if text, _ := tu.Input["text"].(string); text != "" {
				ev.Kind = ai.EventText
				ev.Text = text
			}
		}
		if tu.Tool == "Reasoning" {
			if text, _ := tu.Input["text"].(string); text != "" {
				ev.Kind = ai.EventThinking
				ev.Text = text
			}
		}
		if tu.Tool == "ApiError" {
			ev.Kind = ai.EventError
			ev.Error = firstString(tu.Input, "error", "message", "result")
		}
		out = append(out, ev)
	}
	return out
}

func codexErrorMessage(event history.CodexEvent) string {
	if event.Error != nil && event.Error.Message != "" {
		return event.Error.Message
	}
	if event.Message != "" {
		return event.Message
	}
	return event.Payload.Message
}

func (s *codexCLIState) buildFunctionToolUse(call, output history.CodexEvent) claude.ToolUse {
	return codexToolUse(history.NormalizeCodexToolCall(history.CodexToolCall{
		Name:       call.Payload.Name,
		Namespace:  call.Payload.Namespace,
		Arguments:  call.Payload.Arguments,
		ID:         call.Payload.CallID,
		SessionID:  s.sessionID,
		Response:   history.CodexOutputText(output.Payload.Output),
		RecordType: "response_item.function_call",
	}), s.model)
}

func codexReasoningText(event history.CodexEvent) string {
	if len(event.Payload.Summary) == 0 {
		return event.Payload.Text
	}
	var summaries []history.CodexReasoningSummary
	if err := json.Unmarshal(event.Payload.Summary, &summaries); err == nil {
		var text string
		for _, summary := range summaries {
			if summary.Text != "" {
				text = summary.Text
			}
		}
		if text != "" {
			return text
		}
	}
	var text string
	if err := json.Unmarshal(event.Payload.Summary, &text); err == nil {
		return text
	}
	return event.Payload.Text
}

func codexMessageText(event history.CodexEvent) string {
	if event.Payload.Role != "" && event.Payload.Role != "assistant" {
		return ""
	}
	var text string
	for _, content := range event.Payload.Content {
		if content.Type == "output_text" && content.Text != "" {
			text += content.Text
		}
	}
	return text
}
