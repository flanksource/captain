package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
)

var geminiCLICommand = "gemini"

const geminiCLIDefaultModel = "gemini-3.5-flash"

type GeminiCLI struct {
	model   string
	sandbox *api.SandboxConfig
}

// registry.Google declares Streaming for ModeCLI, and the logging/validating
// middleware type-asserts on this interface before calling ExecuteStream.
var _ ai.StreamingProvider = (*GeminiCLI)(nil)

func NewGeminiCLI(model string) *GeminiCLI {
	if strings.TrimSpace(model) == "" {
		model = geminiCLIDefaultModel
	}
	model = ai.NormalizeModelForBackend(ai.BackendGeminiCLI, model)
	return &GeminiCLI{model: model}
}

func (g *GeminiCLI) GetModel() string       { return g.model }
func (g *GeminiCLI) GetBackend() ai.Backend { return ai.BackendGeminiCLI }

func (g *GeminiCLI) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()
	events, err := g.ExecuteStream(ctx, req)
	if err != nil {
		return nil, err
	}
	resp, err := CoalesceStreamForBackend(ctx, ai.BackendGeminiCLI, g.model, events, start)
	if err != nil {
		return nil, err
	}
	if req.Prompt.Schema != nil {
		raw, _ := resp.StructuredData.(json.RawMessage)
		if err := ai.BindStructuredOutput(req.Prompt.Schema, raw); err != nil {
			return nil, err
		}
		resp.StructuredData = req.Prompt.Schema
		resp.Text = ""
	}
	return resp, nil
}

func (g *GeminiCLI) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	// gemini has no structured-output flag, so the schema rides in the prompt and
	// the reply is validated against it when the terminal result arrives.
	req, schema, err := ai.WithSchemaPrompt(req)
	if err != nil {
		return nil, fmt.Errorf("gemini-cli: cannot derive structured-output schema: %w", err)
	}
	args, err := buildGeminiCLIArgs(g.model, req)
	if err != nil {
		return nil, err
	}
	env := []string(nil)
	if req.Setup != nil {
		env = commandEnv(req.Setup.Env)
	}
	// gemini reads the prompt from stdin and goes headless whenever stdin or
	// stdout is not a TTY, which piping both guarantees.
	cmd, stdout, stderrBuf, closeSandbox, err := startCLIStream(ctx, geminiCLICommand, args, []byte(composePrompt(req)), req.Cwd(), env, g.sandbox)
	if err != nil {
		return nil, err
	}
	out := make(chan ai.Event, 16)
	go func() {
		defer close(out)
		defer closeSandbox()
		defer func() { _ = stdout.Close() }()
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
		state := &geminiCLIState{model: g.model, schema: schema}
		for scanner.Scan() {
			line := bytes.TrimSpace(scanner.Bytes())
			if len(line) == 0 {
				continue
			}
			for _, ev := range state.mapLine(line) {
				emit(ctx, out, ev)
			}
		}
		if err := scanner.Err(); err != nil {
			emit(ctx, out, ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("gemini stream-json: %v", err), Model: g.model})
		}
		if err := finishCLIStream(ctx, cmd, stderrBuf); err != nil {
			emit(ctx, out, ai.Event{Kind: ai.EventError, Error: err.Error(), Model: g.model})
		}
	}()
	return out, nil
}

// buildGeminiCLIArgs maps the request onto gemini's headless flags. The knobs
// gemini has no equivalent for are deliberately absent rather than approximated:
// there is no system-prompt flag (system text is composed into the prompt), no
// per-run MCP override, no memory/hook opt-outs, and --resume takes a session
// index rather than an id — which is why the registry declares no Resume for
// this backend.
func buildGeminiCLIArgs(model string, req ai.Request) ([]string, error) {
	if err := ai.ValidateAttachmentCompatibility([]api.Model{{Name: model, Backend: api.BackendGeminiCLI}}, req.Prompt.Attachments); err != nil {
		return nil, err
	}
	args := []string{"--output-format", "stream-json"}
	if m := strings.TrimSpace(model); m != "" {
		args = append(args, "--model", m)
	}
	if mode := geminiApprovalMode(req.Permissions); mode != "" {
		args = append(args, "--approval-mode", mode)
	}
	return args, nil
}

// geminiApprovalMode maps the permission posture onto gemini's --approval-mode
// choices (default | auto_edit | yolo | plan). The default posture emits no flag
// so gemini keeps its own default, under which a tool needing confirmation fails
// loudly instead of silently running.
func geminiApprovalMode(p api.Permissions) string {
	switch {
	case p.Mode == api.PermissionPlan:
		return "plan"
	case p.Mode == api.PermissionBypass, p.Mode == api.PermissionDontAsk:
		return "yolo"
	case p.Mode == api.PermissionAcceptEdits, p.Mode == api.PermissionAuto:
		return "auto_edit"
	case p.Mode == "" && p.HasPreset(api.PresetEdit):
		return "auto_edit"
	default:
		return ""
	}
}

// geminiStreamEvent is one line of gemini's --output-format stream-json (the
// core StreamJsonFormatter): init | message | tool_use | tool_result | error |
// result, one JSON object per line.
type geminiStreamEvent struct {
	Type      string          `json:"type"`
	SessionID string          `json:"session_id,omitempty"`
	Model     string          `json:"model,omitempty"`
	Role      string          `json:"role,omitempty"`
	Content   string          `json:"content,omitempty"`
	ToolName  string          `json:"tool_name,omitempty"`
	ToolID    string          `json:"tool_id,omitempty"`
	Params    map[string]any  `json:"parameters,omitempty"`
	Status    string          `json:"status,omitempty"`
	Output    string          `json:"output,omitempty"`
	Severity  string          `json:"severity,omitempty"`
	Message   string          `json:"message,omitempty"`
	Error     *geminiError    `json:"error,omitempty"`
	Stats     *geminiRunStats `json:"stats,omitempty"`
}

type geminiError struct {
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
}

// geminiRunStats is convertToStreamStats' output. InputTokens is the gross
// prompt count (cache included) while Input is already net of Cached; captain's
// buckets are disjoint, so the netting happens here rather than trusting either
// field alone.
type geminiRunStats struct {
	TotalTokens  int `json:"total_tokens"`
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	Cached       int `json:"cached"`
	Input        int `json:"input"`
	DurationMS   int `json:"duration_ms"`
	ToolCalls    int `json:"tool_calls"`
}

type geminiCLIState struct {
	model     string
	sessionID string
	schema    json.RawMessage
	text      strings.Builder
}

func (s *geminiCLIState) mapLine(line []byte) []ai.Event {
	var event geminiStreamEvent
	if err := json.Unmarshal(line, &event); err != nil {
		return []ai.Event{{Kind: ai.EventError, Error: fmt.Sprintf("gemini json parse: %v", err), Model: s.model}}
	}
	switch event.Type {
	case "init":
		if event.SessionID != "" {
			s.sessionID = event.SessionID
		}
		if event.Model != "" {
			s.model = event.Model
		}
		return []ai.Event{{Kind: ai.EventSystem, Tool: "SessionInit", SessionID: s.sessionID, Model: s.model}}
	case "message":
		// role=user is gemini echoing the prompt back; only assistant deltas are
		// response text.
		if event.Role != "assistant" || event.Content == "" {
			return nil
		}
		s.text.WriteString(event.Content)
		return []ai.Event{{Kind: ai.EventText, Text: event.Content, SessionID: s.sessionID, Model: s.model}}
	case "tool_use":
		return []ai.Event{{
			Kind:       ai.EventToolUse,
			Tool:       event.ToolName,
			Input:      event.Params,
			ToolCallID: event.ToolID,
			SessionID:  s.sessionID,
			Model:      s.model,
		}}
	case "tool_result":
		ev := ai.Event{
			Kind:       ai.EventToolResult,
			Text:       event.Output,
			ToolCallID: event.ToolID,
			Success:    event.Status != "error",
			SessionID:  s.sessionID,
			Model:      s.model,
		}
		if !ev.Success {
			ev.Error = geminiErrorMessage(event)
			if ev.Text == "" {
				ev.Text = ev.Error
			}
		}
		return []ai.Event{ev}
	case "error":
		return []ai.Event{{Kind: ai.EventError, Error: geminiErrorMessage(event), SessionID: s.sessionID, Model: s.model}}
	case "result":
		return s.resultEvents(event)
	}
	return nil
}

func (s *geminiCLIState) resultEvents(event geminiStreamEvent) []ai.Event {
	ev := ai.Event{
		Kind:      ai.EventResult,
		Tool:      "Result",
		Success:   event.Status != "error",
		SessionID: s.sessionID,
		Model:     s.model,
		Usage:     geminiUsage(event.Stats),
	}
	if !ev.Success {
		ev.Error = geminiErrorMessage(event)
		return []ai.Event{ev}
	}
	structured, err := ai.ValidatedStructuredData(s.schema, s.text.String(), nil)
	if err != nil {
		ev.Success = false
		ev.Error = err.Error()
		return []ai.Event{{Kind: ai.EventError, Error: err.Error(), SessionID: s.sessionID, Model: s.model}, ev}
	}
	ev.StructuredData = structured
	return []ai.Event{ev}
}

func geminiUsage(stats *geminiRunStats) *ai.Usage {
	if stats == nil {
		return nil
	}
	return &ai.Usage{
		InputTokens:     ai.NetInputTokens(stats.InputTokens, stats.Cached),
		OutputTokens:    stats.OutputTokens,
		CacheReadTokens: stats.Cached,
	}
}

func geminiErrorMessage(event geminiStreamEvent) string {
	if event.Error != nil && event.Error.Message != "" {
		return event.Error.Message
	}
	if event.Message != "" {
		return event.Message
	}
	if event.Output != "" {
		return event.Output
	}
	return fmt.Sprintf("gemini reported a %s failure", firstNonEmpty(event.Type, "run"))
}
