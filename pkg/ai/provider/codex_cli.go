package provider

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/commons/logger"
)

type CodexCLI struct {
	model string
}

// CodexCLIDefaultModel is the default model used when the caller does not
// specify one. Empty means "let `codex` pick its own default" — captain no
// longer ships a hard-coded id since the static model catalog was removed
// in favour of live `/v1/models` listings.
const CodexCLIDefaultModel = ""

func NewCodexCLI(model string) *CodexCLI {
	if model == "" {
		model = CodexCLIDefaultModel
	}
	return &CodexCLI{model: model}
}

func (c *CodexCLI) GetModel() string       { return c.model }
func (c *CodexCLI) GetBackend() ai.Backend { return ai.BackendCodexCLI }

// Execute drains the streaming output and returns the final aggregated text.
// Buffered callers should prefer ExecuteStream when they want live events.
func (c *CodexCLI) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()

	timeout := 120 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		if ctxTimeout := time.Until(deadline); ctxTimeout < timeout {
			timeout = ctxTimeout
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	events, err := c.ExecuteStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return CoalesceStream(ctx, c.model, events, start)
}

// ExecuteStream spawns `codex exec --json ...` and publishes parsed JSONL
// events as ai.Event values. The channel closes when the subprocess exits or
// ctx is cancelled.
func (c *CodexCLI) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	args, err := buildCodexExecArgs(c.model, req)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "codex", args...)
	cmd.Env = clearNestingEnv(os.Environ())
	if req.Cwd != "" {
		cmd.Dir = req.Cwd
	}

	logger.Debugf("[codex-cli] exec: codex %s", strings.Join(redactCodexArgs(args), " "))

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		_ = stdoutPipe.Close()
		return nil, fmt.Errorf("failed to create stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		if IsCommandNotFound(err) {
			return nil, fmt.Errorf("%w: %v", ai.ErrCLINotFound, err)
		}
		return nil, fmt.Errorf("failed to start codex: %w", err)
	}

	events := make(chan ai.Event, 16)

	var stderrBuf strings.Builder
	var stderrMu sync.Mutex
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		data, _ := io.ReadAll(stderrPipe)
		stderrMu.Lock()
		stderrBuf.Write(data)
		stderrMu.Unlock()
	}()

	go func() {
		defer close(events)

		scanner := bufio.NewScanner(stdoutPipe)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || !strings.HasPrefix(line, "{") {
				continue
			}
			ev, ok := mapCodexEvent(line, c.model)
			if !ok {
				continue
			}
			select {
			case events <- ev:
			case <-ctx.Done():
				_ = cmd.Process.Kill()
				return
			}
		}
		if err := scanner.Err(); err != nil {
			select {
			case events <- ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("codex stream parse: %v", err)}:
			case <-ctx.Done():
			}
		}

		<-stderrDone
		waitErr := cmd.Wait()
		stderrMu.Lock()
		stderrText := stderrBuf.String()
		stderrMu.Unlock()
		if waitErr != nil {
			if stderrText != "" {
				logger.Debugf("[codex-cli stderr]\n%s", truncate(stderrText, 4096))
			}
			select {
			case events <- ai.Event{
				Kind:  ai.EventError,
				Error: HandleExitError(GetExitCode(waitErr), ParseStderr(stderrText)).Error(),
			}:
			case <-ctx.Done():
			}
		} else if stderrText != "" && logger.IsTraceEnabled() {
			logger.Tracef("[codex-cli stderr]\n%s", truncate(stderrText, 4096))
		}
	}()

	return events, nil
}

// buildCodexExecArgs assembles argv for `codex exec --json`, translating the
// provider-agnostic ai.Request safety knobs into codex's flag surface. Unknown
// claude-only flags (AllowedTools, DisallowedTools, NoSkills, SkillDirs,
// AppendSystemPrompt) are silently dropped — we surface a soft warning at the
// caller layer rather than failing here.
func buildCodexExecArgs(model string, req ai.Request) ([]string, error) {
	args := []string{"exec", "--json", "--skip-git-repo-check"}

	if model != "" {
		args = append(args, "--model", model)
	}
	if req.ReasoningEffort != "" {
		args = append(args, "-c", fmt.Sprintf(`model_reasoning_effort=%q`, req.ReasoningEffort))
	}

	if req.Edit && req.PermissionMode == "" {
		args = append(args, "--sandbox", "workspace-write")
	}
	if req.PermissionMode == "bypassPermissions" {
		args = append(args, "--dangerously-bypass-approvals-and-sandbox")
	}
	if req.NoMCP {
		args = append(args, "-c", "mcp_servers={}")
	}
	if req.NoUser || req.Bare {
		args = append(args, "--ignore-user-config")
	}
	if req.NoHooks || req.NoProject || req.Bare {
		args = append(args, "--ignore-rules")
	}
	if req.NoMemory || req.Bare {
		args = append(args, "--ephemeral")
	}

	prompt := req.Prompt
	if req.SystemPrompt != "" {
		prompt = req.SystemPrompt + "\n\n" + prompt
	}
	if req.AppendSystemPrompt != "" {
		prompt = prompt + "\n\n" + req.AppendSystemPrompt
	}
	args = append(args, prompt)
	return args, nil
}

// codexEvent is a permissive subset of the JSON envelope codex emits with
// --json. Codex emits two related schemas across versions:
//   - the legacy "msg" envelope: `{"id":..,"msg":{"type":"agent_message",..}}`
//   - the newer dotted "type" envelope: `{"type":"thread.started","thread_id":..}`,
//     `{"type":"item.completed","item":{"text":..}}`, `{"type":"turn.completed",..}`,
//     `{"type":"turn.failed","error":{"message":..}}`, top-level `{"type":"error","message":..}`.
//
// We unmarshal into a permissive shape that captures both, then dispatch on
// the discriminator. Top-level fields like `message`, `thread_id`, `item`, and
// `error` are read directly so callers don't need to know which schema codex
// happened to emit on a given run.
type codexEvent struct {
	ID       string          `json:"id"`
	Type     string          `json:"type"`
	ThreadID string          `json:"thread_id"`
	Message  string          `json:"message"` // top-level error envelope
	Msg      *codexMsg       `json:"msg"`
	Item     *codexItem      `json:"item"`
	Error    *codexError     `json:"error"`
	Usage    *codexUsage     `json:"usage"`
	Data     json.RawMessage `json:"data"`
}

type codexMsg struct {
	Type    string          `json:"type"`
	Message string          `json:"message"`
	Text    string          `json:"text"`
	Delta   string          `json:"delta"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Error   string          `json:"error"`
	Usage   *codexUsage     `json:"usage"`
	Cost    float64         `json:"cost_usd"`
}

type codexItem struct {
	Type    string `json:"type"`
	Text    string `json:"text"`
	Content string `json:"content"`
	Name    string `json:"name"`
}

type codexError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
	Status  int    `json:"status"`
}

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// extractCodexErrorText pulls a human-readable message out of codex's error
// envelopes. Codex sometimes nests JSON-encoded payloads inside the `message`
// field (e.g. an OpenAI 400 served back as a stringified error envelope), so we
// best-effort unwrap one level when the message itself parses as JSON.
func extractCodexErrorText(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.HasPrefix(raw, "{") {
		return raw
	}
	var nested codexError
	wrapper := struct {
		Error   *codexError `json:"error"`
		Message string      `json:"message"`
	}{Error: &nested}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
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

// mapCodexEvent maps one JSONL line into an ai.Event. Returns ok=false when
// the line is metadata captain doesn't surface (heartbeats, debug, etc).
func mapCodexEvent(line, model string) (ai.Event, bool) {
	var ev codexEvent
	if err := json.Unmarshal([]byte(line), &ev); err != nil {
		return ai.Event{}, false
	}
	kind := ev.Type
	var msg codexMsg
	if ev.Msg != nil {
		msg = *ev.Msg
		if kind == "" {
			kind = msg.Type
		}
	}
	switch kind {
	case "agent_message", "message", "assistant_message":
		text := firstNonEmpty(msg.Message, msg.Text)
		if text == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventText, Text: text, Model: model}, true
	case "agent_message_delta", "message_delta":
		if msg.Delta == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventText, Text: msg.Delta, Model: model}, true
	case "tool_call", "tool_use", "exec_command_begin":
		input := map[string]any{}
		if len(msg.Input) > 0 {
			_ = json.Unmarshal(msg.Input, &input)
		}
		out := ai.Event{Kind: ai.EventToolUse, Tool: msg.Name, Input: input, Model: model}
		out.Raw = codexToolUse(msg.Name, input, ev.ID, model)
		return out, true
	case "task_complete", "result", "completion":
		out := ai.Event{Kind: ai.EventResult, Tool: "Result", Model: model, Success: msg.Error == "", CostUSD: msg.Cost}
		if msg.Usage != nil {
			out.Usage = &ai.Usage{InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens}
		}
		if msg.Error != "" {
			out.Error = msg.Error
		}
		out.Raw = codexResultToolUse(out, ev.ID)
		return out, true
	case "error", "task_error":
		errText := firstNonEmpty(msg.Error, msg.Message, ev.Message)
		if ev.Error != nil {
			errText = firstNonEmpty(ev.Error.Message, errText)
		}
		return ai.Event{Kind: ai.EventError, Error: extractCodexErrorText(errText), Model: model}, true
	case "session_configured", "session_init":
		out := ai.Event{Kind: ai.EventSystem, Tool: "SessionInit", SessionID: ev.ID, Model: model}
		out.Raw = codexSessionToolUse(ev.ID, model)
		return out, true

	// --- Newer dotted-type schema ---
	case "thread.started":
		sessionID := firstNonEmpty(ev.ThreadID, ev.ID)
		out := ai.Event{Kind: ai.EventSystem, Tool: "SessionInit", SessionID: sessionID, Model: model}
		out.Raw = codexSessionToolUse(sessionID, model)
		return out, true
	case "turn.started":
		// Heartbeat — captain does not surface this.
		return ai.Event{}, false
	case "item.started", "item.delta":
		// Streaming partials; drop until item.completed to keep parity with
		// the legacy schema's coarse-grained text events.
		return ai.Event{}, false
	case "item.completed":
		if ev.Item == nil {
			return ai.Event{}, false
		}
		text := firstNonEmpty(ev.Item.Text, ev.Item.Content)
		if text == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventText, Text: text, Model: model}, true
	case "turn.completed":
		out := ai.Event{Kind: ai.EventResult, Tool: "Result", Model: model, Success: true}
		if ev.Usage != nil {
			out.Usage = &ai.Usage{InputTokens: ev.Usage.InputTokens, OutputTokens: ev.Usage.OutputTokens}
		}
		out.Raw = codexResultToolUse(out, firstNonEmpty(ev.ThreadID, ev.ID))
		return out, true
	case "turn.failed":
		var errText string
		if ev.Error != nil {
			errText = ev.Error.Message
		}
		errText = firstNonEmpty(errText, ev.Message)
		return ai.Event{Kind: ai.EventError, Error: extractCodexErrorText(errText), Model: model}, true
	}
	return ai.Event{}, false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// codexToolUse builds the claude.ToolUse stand-in that mapCodexEvent stashes
// on ai.Event.Raw so the shared lineRenderer in pkg/cli renders codex live
// streams identically to `captain history` output. Source is hard-coded to
// "codex" so sessionKey/sessionHeaderText pick the codex icon and label.
func codexToolUse(name string, input map[string]any, sessionID, model string) claude.ToolUse {
	return claude.ToolUse{
		Tool:      name,
		Input:     input,
		SessionID: sessionID,
		Source:    "codex",
		Model:     model,
	}
}

// codexResultToolUse mirrors the synthetic Result row that
// pkg/cli/ai.go's renderResultEvent constructs for Claude, preserving cost,
// usage, and error state so the "🏁 result …" footer renders consistently.
func codexResultToolUse(ev ai.Event, sessionID string) claude.ToolUse {
	input := map[string]any{}
	for k, v := range ev.Input {
		input[k] = v
	}
	if ev.CostUSD > 0 {
		if _, ok := input["total_cost_usd"]; !ok {
			input["total_cost_usd"] = ev.CostUSD
		}
	}
	tu := claude.ToolUse{
		Tool:      "Result",
		Input:     input,
		SessionID: sessionID,
		Source:    "codex",
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

// codexSessionToolUse builds the synthetic SessionInit row used by the
// shared renderer to emit a session-start banner the first time a sessionKey
// is seen. The lineRenderer treats SessionInit just like any other tool row.
func codexSessionToolUse(sessionID, model string) claude.ToolUse {
	return claude.ToolUse{
		Tool:      "SessionInit",
		SessionID: sessionID,
		Source:    "codex",
		Model:     model,
	}
}

// redactCodexArgs returns a copy of args with the trailing prompt truncated so
// debug logs stay readable. The prompt is the only positional argument and
// always sits at the end of the argv built by buildCodexExecArgs.
func redactCodexArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, len(args))
	copy(out, args)
	last := len(out) - 1
	if !strings.HasPrefix(out[last], "-") {
		out[last] = truncate(out[last], 120)
	}
	return out
}
