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
)

type CodexCLI struct {
	model string
}

func NewCodexCLI(model string) *CodexCLI {
	if model == "" {
		model = "codex"
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
		if waitErr != nil {
			stderrMu.Lock()
			stderrText := stderrBuf.String()
			stderrMu.Unlock()
			select {
			case events <- ai.Event{
				Kind:  ai.EventError,
				Error: HandleExitError(GetExitCode(waitErr), ParseStderr(stderrText)).Error(),
			}:
			case <-ctx.Done():
			}
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
// --json. Codex events carry a "type" or "msg.type" discriminator; we accept
// either to insulate captain from minor shape changes across codex versions.
type codexEvent struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Msg  *codexMsg       `json:"msg"`
	Data json.RawMessage `json:"data"`
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

type codexUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
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
		return ai.Event{Kind: ai.EventToolUse, Tool: msg.Name, Input: input, Model: model}, true
	case "task_complete", "result", "completion":
		out := ai.Event{Kind: ai.EventResult, Tool: "Result", Model: model, Success: msg.Error == "", CostUSD: msg.Cost}
		if msg.Usage != nil {
			out.Usage = &ai.Usage{InputTokens: msg.Usage.InputTokens, OutputTokens: msg.Usage.OutputTokens}
		}
		if msg.Error != "" {
			out.Error = msg.Error
		}
		return out, true
	case "error", "task_error":
		return ai.Event{Kind: ai.EventError, Error: firstNonEmpty(msg.Error, msg.Message), Model: model}, true
	case "session_configured", "session_init":
		return ai.Event{Kind: ai.EventSystem, Tool: "SessionInit", SessionID: ev.ID, Model: model}, true
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
