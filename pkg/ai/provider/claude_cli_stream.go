package provider

import (
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

// ExecuteStream spawns `claude -p ... --output-format stream-json` and
// publishes each parsed line as an ai.Event on the returned channel. The
// channel closes when the subprocess exits or ctx is cancelled.
//
// The provider satisfies ai.StreamingProvider; ai.RunUntil and other loop
// drivers can therefore drive it without a buffered round-trip. Callers that
// only want the final answer can drain the channel and look at the last
// EventResult, but Execute already does that — prefer Execute for
// non-streaming use.
func (c *ClaudeCLI) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	if req.StructuredOutput != nil {
		return nil, fmt.Errorf("claude_cli stream mode does not support StructuredOutput; use Execute")
	}

	args, err := buildClaudeStreamArgs(c.model, req)
	if err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Env = clearNestingEnv(os.Environ())

	logger.Debugf("[claude-cli] exec: claude %s", strings.Join(redactClaudeArgs(args), " "))

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
		return nil, fmt.Errorf("failed to start claude: %w", err)
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

		it := claude.NewStreamJSONIterator(stdoutPipe)
		for it.Next() {
			entry := it.Entry()
			for _, ev := range mapHistoryEntry(entry, c.model) {
				select {
				case events <- ev:
				case <-ctx.Done():
					_ = cmd.Process.Kill()
					return
				}
			}
		}
		if err := it.Err(); err != nil {
			select {
			case events <- ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("stream parse: %v", err)}:
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
				logger.Debugf("[claude-cli stderr]\n%s", truncate(stderrText, 4096))
			}
			select {
			case events <- ai.Event{
				Kind:  ai.EventError,
				Error: HandleExitError(GetExitCode(waitErr), ParseStderr(stderrText)).Error(),
			}:
			case <-ctx.Done():
			}
		} else if stderrText != "" && logger.IsTraceEnabled() {
			logger.Tracef("[claude-cli stderr]\n%s", truncate(stderrText, 4096))
		}
	}()

	return events, nil
}

// buildClaudeStreamArgs assembles the argv for a stream-json invocation.
// Stream-json requires --verbose; we force it on regardless of req.Verbose
// so callers cannot silently produce an empty stream by leaving it off.
//
// Returns an error for contradictory flag combinations (e.g. --no-memory
// without --bare, since claude has no flag to disable auto-memory alone).
func buildClaudeStreamArgs(model string, req ai.Request) ([]string, error) {
	if err := validateClaudeRequest(req); err != nil {
		return nil, err
	}

	args := []string{
		"-p",
		"--output-format", "stream-json",
		"--verbose",
		"--model", MapClaudeCodeModel(model),
	}

	mode := req.PermissionMode
	allowed := req.AllowedTools
	if req.Edit {
		if mode == "" {
			mode = "acceptEdits"
		}
		if len(allowed) == 0 {
			allowed = SafeEditAllowlist
		}
	}
	mode = DemoteOnAllowlist(mode, len(allowed) > 0)

	if mode != "" {
		args = append(args, "--permission-mode", mode)
	}
	if len(allowed) > 0 {
		args = append(args, "--allowedTools")
		args = append(args, allowed...)
	}
	if len(req.DisallowedTools) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, req.DisallowedTools...)
	}
	if req.NoMCP {
		args = append(args, "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`)
	} else if req.StrictMCP {
		args = append(args, "--strict-mcp-config")
	}
	if req.NoSkills {
		args = append(args, "--disable-slash-commands")
	}
	for _, dir := range req.SkillDirs {
		args = append(args, "--plugin-dir", dir)
	}
	if sources := claudeSettingSources(req); sources != nil {
		args = append(args, "--setting-sources", strings.Join(sources, ","))
	}
	if req.Bare {
		args = append(args, "--bare")
	}
	if req.MaxTurns > 0 {
		args = append(args, "--max-turns", fmt.Sprintf("%d", req.MaxTurns))
	}
	if req.SessionID != "" {
		args = append(args, "--resume", req.SessionID)
	}
	if req.SystemPrompt != "" {
		args = append(args, "--system-prompt", req.SystemPrompt)
	}
	if req.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", req.AppendSystemPrompt)
	}
	args = append(args, req.Prompt)
	return args, nil
}

// validateClaudeRequest rejects flag combinations claude CLI cannot express.
// Auto-memory and hooks have no per-feature kill switches; they ride along
// with --bare or with the relevant --setting-sources level. Surface a clear
// error rather than silently dropping the user's intent.
func validateClaudeRequest(req ai.Request) error {
	if req.NoMemory && !req.Bare {
		return fmt.Errorf("--no-memory requires --bare for claude (no per-feature flag exists; use --bare)")
	}
	if req.NoHooks && !req.Bare && !req.NoUser && !req.NoProject {
		return fmt.Errorf("--no-hooks requires --bare, --no-user, or --no-project for claude (hooks are loaded from settings.json files)")
	}
	return nil
}

// claudeSettingSources returns the list to pass to --setting-sources, or nil
// when no override is needed (claude defaults to user,project,local).
func claudeSettingSources(req ai.Request) []string {
	if !req.NoUser && !req.NoProject {
		return nil
	}
	sources := []string{}
	if !req.NoUser {
		sources = append(sources, "user")
	}
	if !req.NoProject {
		sources = append(sources, "project", "local")
	}
	return sources
}

// mapHistoryEntry converts one captain claude.HistoryEntry into zero or more
// ai.Event values. The history reader already turns stream-json system/result
// lines into synthetic tool_use blocks, so the dispatcher here only needs to
// inspect content blocks and the synthetic Name field.
func mapHistoryEntry(entry claude.HistoryEntry, fallbackModel string) []ai.Event {
	if entry.Message.Role != claude.MessageRoleAssistant && entry.Message.Role != claude.MessageRoleUser {
		return nil
	}

	model := entry.Message.Model
	if model == "" {
		model = fallbackModel
	}

	var out []ai.Event
	for _, block := range entry.Message.Content {
		if ev, ok := mapContentBlock(block, entry, model); ok {
			ev.Raw = entry
			out = append(out, ev)
		}
	}
	return out
}

func mapContentBlock(block claude.ContentBlock, entry claude.HistoryEntry, model string) (ai.Event, bool) {
	switch block.Type {
	case claude.ContentTypeText:
		if block.Text == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventText, Text: block.Text, Model: model}, true

	case claude.ContentTypeThinking, claude.ContentTypeRedactedThinking:
		text := block.Thinking
		if text == "" {
			text = block.Text
		}
		if text == "" {
			return ai.Event{}, false
		}
		return ai.Event{Kind: ai.EventThinking, Text: text, Model: model}, true

	case claude.ContentTypeToolUse,
		claude.ContentTypeServerToolUse,
		claude.ContentTypeMCPToolUse:
		return mapToolUseBlock(block, entry, model), true
	}
	return ai.Event{}, false
}

// mapToolUseBlock handles both real tool uses and the synthetic ones the
// reader emits for stream-json system/result lines.
func mapToolUseBlock(block claude.ContentBlock, entry claude.HistoryEntry, model string) ai.Event {
	input := decodeToolInput(block.Input)

	switch block.Name {
	case "Result":
		ev := ai.Event{Kind: ai.EventResult, Tool: block.Name, Input: input, Model: model}
		if isErr, ok := input["is_error"].(bool); ok {
			ev.Success = !isErr
		} else {
			ev.Success = true
		}
		if cost, ok := input["total_cost_usd"].(float64); ok {
			ev.CostUSD = cost
		}
		if usage := decodeUsageMap(input["usage"]); usage != nil {
			ev.Usage = usage
		}
		if err, ok := input["error"].(string); ok && err != "" {
			ev.Error = err
		}
		return ev

	case "SessionInit":
		ev := ai.Event{Kind: ai.EventSystem, Tool: block.Name, Input: input, Model: model}
		if sid, ok := input["session_id"].(string); ok {
			ev.SessionID = sid
		}
		if sid := entry.SessionID; sid != "" && ev.SessionID == "" {
			ev.SessionID = sid
		}
		if m, ok := input["model"].(string); ok && m != "" {
			ev.Model = m
		}
		return ev

	case "ApiError":
		msg, _ := input["error"].(string)
		return ai.Event{Kind: ai.EventError, Tool: block.Name, Input: input, Error: msg, Model: model}

	case "ParseError":
		msg, _ := input["error"].(string)
		return ai.Event{Kind: ai.EventError, Tool: block.Name, Input: input, Error: msg, Model: model}
	}

	return ai.Event{
		Kind:  ai.EventToolUse,
		Tool:  block.Name,
		Input: input,
		Model: model,
	}
}

func decodeToolInput(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}

func decodeUsageMap(v any) *ai.Usage {
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return nil
	}
	u := &ai.Usage{}
	if x, ok := m["input_tokens"].(float64); ok {
		u.InputTokens = int(x)
	}
	if x, ok := m["output_tokens"].(float64); ok {
		u.OutputTokens = int(x)
	}
	if x, ok := m["cache_read_input_tokens"].(float64); ok {
		u.CacheReadTokens = int(x)
	}
	if x, ok := m["cache_creation_input_tokens"].(float64); ok {
		u.CacheWriteTokens = int(x)
	}
	if x, ok := m["reasoning_tokens"].(float64); ok {
		u.ReasoningTokens = int(x)
	}
	return u
}

// CoalesceStream drains an event channel and returns the equivalent buffered
// ai.Response. Loop drivers that want a per-iteration "final answer" alongside
// the live event stream call this on a tee'd channel; for one-shot use, prefer
// Provider.Execute.
func CoalesceStream(ctx context.Context, model string, events <-chan ai.Event, start time.Time) (*ai.Response, error) {
	var (
		text       strings.Builder
		usage      ai.Usage
		lastResult *ai.Event
		errEvents  []ai.Event
		sessionID  string
	)

	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return finaliseCoalescedResponse(model, text.String(), usage, lastResult, errEvents, sessionID, start)
			}
			switch ev.Kind {
			case ai.EventText:
				text.WriteString(ev.Text)
			case ai.EventResult:
				cp := ev
				lastResult = &cp
				if ev.Usage != nil {
					usage = *ev.Usage
				}
			case ai.EventSystem:
				if ev.SessionID != "" {
					sessionID = ev.SessionID
				}
			case ai.EventError:
				errEvents = append(errEvents, ev)
			}
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: context cancelled", ai.ErrTimeout)
		}
	}
}

func finaliseCoalescedResponse(model, text string, usage ai.Usage, lastResult *ai.Event, errEvents []ai.Event, sessionID string, start time.Time) (*ai.Response, error) {
	if lastResult != nil && !lastResult.Success {
		msg := lastResult.Error
		if msg == "" {
			msg = "claude returned is_error=true"
		}
		return nil, fmt.Errorf("%w: %s", ai.ErrCLIExecutionFailed, msg)
	}
	if lastResult == nil && len(errEvents) > 0 {
		return nil, fmt.Errorf("%w: %s", ai.ErrCLIExecutionFailed, errEvents[len(errEvents)-1].Error)
	}

	resp := &ai.Response{
		Text:     text,
		Model:    model,
		Backend:  ai.BackendClaudeCLI,
		Usage:    usage,
		Duration: time.Since(start),
	}
	if lastResult != nil {
		resp.Raw = lastResult.Input
	}
	if sessionID != "" {
		if resp.Raw == nil {
			resp.Raw = map[string]any{"session_id": sessionID}
		}
	}
	return resp, nil
}

// redactClaudeArgs returns a copy of args with the trailing prompt truncated so
// debug logs stay readable. The prompt is the only positional argument and
// always sits at the end of the argv built by buildClaudeStreamArgs.
func redactClaudeArgs(args []string) []string {
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
