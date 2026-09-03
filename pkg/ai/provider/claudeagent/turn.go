package claudeagent

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/exec"
)

// turnState carries the channels a single in-flight turn uses to receive mapped
// events from the JSON-RPC notification handler. inbox is the handler->turn
// path; term is closed by the handler on a terminal notification; quit is closed
// by the turn goroutine on exit so a late handler send does not block.
//
// ctx and canUseTool let the server-request handler (onRequest, which runs on its
// own goroutine) reach the turn's deadline and permission callback so a
// can_use_tool round-trip is scoped to the turn and unblocks on cancellation.
type turnState struct {
	inbox chan ai.Event
	term  chan struct{}
	quit  chan struct{}

	ctx        context.Context
	canUseTool ai.PermissionFunc
	// planMode marks a plan-only turn: ExitPlanMode is then the terminal signal
	// and its can_use_tool is answered by the shared policy, never the broker.
	planMode bool

	promptMu sync.Mutex
	pending  int
	ended    bool

	interrupting atomic.Bool
}

type promptParams struct {
	Text        string             `json:"text"`
	Attachments []promptAttachment `json:"attachments,omitempty"`
}

type promptAttachment struct {
	MediaType string `json:"mediaType"`
	Data      string `json:"data"`
	Filename  string `json:"filename,omitempty"`
}

func composePrompt(req ai.Request) string {
	return req.Prompt.User
}

func buildPromptParams(req ai.Request) (promptParams, error) {
	params := promptParams{Text: composePrompt(req), Attachments: make([]promptAttachment, 0, len(req.Prompt.Attachments))}
	for i, attachment := range req.Prompt.Attachments {
		content, ok := attachment.PreparedContent()
		if !ok {
			return promptParams{}, fmt.Errorf("attachment %d (%s) is not prepared", i+1, attachment.ID)
		}
		data := content.Bytes
		if data == nil && content.Path != "" {
			var err error
			data, err = os.ReadFile(content.Path)
			if err != nil {
				return promptParams{}, fmt.Errorf("read prepared attachment %s: %w", attachment.ID, err)
			}
		}
		params.Attachments = append(params.Attachments, promptAttachment{
			MediaType: attachment.MediaType,
			Data:      base64.StdEncoding.EncodeToString(data),
			Filename:  attachment.Filename,
		})
	}
	return params, nil
}

// runTurn owns one turn's event channel: it sends the prompt, forwards mapped
// notifications, and emits the terminal result (or a loud error on cancellation
// / mid-turn process exit).
func (p *Provider) runTurn(ctx context.Context, req ai.Request, events chan ai.Event) {
	defer close(events)

	p.turnMu.Lock()
	defer p.turnMu.Unlock()

	ts := &turnState{
		inbox:      make(chan ai.Event, 16),
		term:       make(chan struct{}),
		quit:       make(chan struct{}),
		ctx:        ctx,
		canUseTool: p.cfg.CanUseTool,
		planMode:   req.Permissions.Mode == api.PermissionPlan,
		pending:    1,
	}
	p.setActive(ts)
	defer func() {
		close(ts.quit)
		p.clearActive()
	}()

	if err := ai.ValidateAttachmentCompatibility([]api.Model{{Name: p.model, Provider: api.Anthropic, Mode: api.ModeAgent}}, req.Prompt.Attachments); err != nil {
		emit(ctx, events, ai.Event{Kind: ai.EventError, Error: err.Error(), Model: p.model})
		return
	}
	params, err := buildPromptParams(req)
	if err != nil {
		emit(ctx, events, ai.Event{Kind: ai.EventError, Error: err.Error(), Model: p.model})
		return
	}
	if _, err := p.rpc.Call(ctx, methodPrompt, params); err != nil {
		emit(ctx, events, ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("claude-agent prompt failed: %v", err), Model: p.model})
		return
	}

	for {
		select {
		case ev := <-ts.inbox:
			if !emit(ctx, events, p.withCapturedOutput(ev)) {
				return
			}
		case <-ts.term:
			p.drainInbox(ctx, ts.inbox, events)
			return
		case <-ctx.Done():
			p.interrupt()
			emit(context.Background(), events, ai.Event{Kind: ai.EventError, Error: "claude-agent: context cancelled", Model: p.model})
			return
		case <-p.baseCtx.Done():
			emit(context.Background(), events, ai.Event{Kind: ai.EventError, Error: "claude-agent: provider closed", Model: p.model})
			return
		case <-p.procExited:
			emit(context.Background(), events, ai.Event{Kind: ai.EventError, Error: p.withProcessOutput("claude-agent: process exited mid-turn"), Model: p.model})
			return
		}
	}
}

// onNotification routes a server notification to the active turn. It runs on the
// jsonrpc read loop goroutine, so it must not block indefinitely: sends select
// on the turn's quit and the provider's base context.
func (p *Provider) onNotification(method string, params json.RawMessage) {
	ev, ok := mapNotification(method, params, p.model)
	if ok && ev.SessionID != "" {
		p.rememberSession(ev.SessionID)
	}

	p.activeMu.Lock()
	ts := p.active
	p.activeMu.Unlock()
	if ts == nil {
		return
	}

	if ts.interrupting.Load() && (method == notifyTurnDone || method == notifyTurnError) {
		ok = false
	}
	if ok {
		select {
		case ts.inbox <- ev:
		case <-ts.quit:
		case <-p.baseCtx.Done():
		}
	}
	if method == notifyTurnDone || method == notifyTurnError {
		ts.completePrompt()
	}
}

// withCapturedOutput attaches the child's captured stdio to an error event.
//
// It runs on the turn's own goroutine, never the jsonrpc read loop: the wait in
// withProcessOutput ends when the child's pumps drain, and draining stdout is
// the read loop's own job — enriching there would make the wait outlive its
// cause.
func (p *Provider) withCapturedOutput(ev ai.Event) ai.Event {
	if ev.Kind == ai.EventError {
		ev.Error = p.withProcessOutput(ev.Error)
	}
	return ev
}

// withProcessOutput appends the child's captured stdio to a terminal
// diagnostic.
//
// stdout and stderr are pumped independently, so the turn/error announcing a
// subprocess failure arrives before the stderr flush that explains it: the
// message then carries the JSON-RPC transcript and none of the subprocess's own
// output — an authentication detail written to stderr goes missing from the very
// error raised to report it. awaitStderr reconciles the two pipes first.
func (p *Provider) withProcessOutput(message string) string {
	p.procMu.RLock()
	proc := p.proc
	p.procMu.RUnlock()
	if proc == nil {
		return message
	}
	p.awaitStderr(proc)

	stdout := outputTail(proc.GetStdout())
	stderr := outputTail(proc.GetStderr())
	if stdout == "" && stderr == "" {
		return message
	}

	var detail strings.Builder
	detail.WriteString(message)
	if stdout != "" {
		detail.WriteString("\nstdout (JSON-RPC):\n")
		detail.WriteString(stdout)
	}
	if stderr != "" {
		detail.WriteString("\nstderr:\n")
		detail.WriteString(stderr)
	}
	return detail.String()
}

// awaitStderr gives the child's stderr pump a bounded chance to land before its
// output is read. It returns the moment stderr has anything, or the child is
// gone and no more is coming — a diagnostic never waits on a pipe that already
// spoke.
func (p *Provider) awaitStderr(proc *exec.Process) {
	deadline := time.Now().Add(processOutputDrain)
	for proc.GetStderr() == "" && time.Now().Before(deadline) {
		select {
		case <-p.procExited:
			return
		case <-p.baseCtx.Done():
			return
		case <-time.After(processOutputPoll):
		}
	}
}

func outputTail(output string) string {
	output = strings.TrimSpace(output)
	if len(output) <= errorOutputLimit {
		return output
	}
	return "[truncated to last 16 KiB]\n" + output[len(output)-errorOutputLimit:]
}

// onRequest answers a server→client request from agent.ts. It runs on its own
// goroutine (per jsonrpc.Handlers.OnRequest), so blocking on a human approval is
// safe. The only request is can_use_tool; unknown methods get method-not-found.
func (p *Provider) onRequest(method string, params json.RawMessage) (any, *jsonrpc.RPCError) {
	if method != methodCanUseTool {
		return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found: " + method}
	}
	return p.handleCanUseTool(params)
}

// canUseToolParams is the agent.ts can_use_tool request payload.
type canUseToolParams struct {
	Tool      string         `json:"tool"`
	Input     map[string]any `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
}

// canUseToolResult is the decision agent.ts maps onto an SDK PermissionResult.
type canUseToolResult struct {
	Allow        bool           `json:"allow"`
	Message      string         `json:"message,omitempty"`
	UpdatedInput map[string]any `json:"updatedInput,omitempty"`
}

// handleCanUseTool routes a tool-permission request to the active turn's
// CanUseTool callback, surfacing an EventPermission so callers can observe what
// is awaiting approval. With no callback (or no active turn) it allows the tool,
// matching the bypass default for non-brokered runs.
func (p *Provider) handleCanUseTool(params json.RawMessage) (any, *jsonrpc.RPCError) {
	var in canUseToolParams
	if err := json.Unmarshal(params, &in); err != nil {
		return nil, &jsonrpc.RPCError{Code: -32602, Message: "invalid can_use_tool params: " + err.Error()}
	}

	p.activeMu.Lock()
	ts := p.active
	p.activeMu.Unlock()
	if ts == nil {
		return canUseToolResult{Allow: true, UpdatedInput: in.Input}, nil
	}

	p.sessMu.Lock()
	sessionID := p.sessionID
	p.sessMu.Unlock()

	// The plan-mode terminal signal is answered here, never brokered: nothing is
	// awaiting a human, so no EventPermission is surfaced either. The tool_use
	// already streamed, so the turn still ends in a plan terminal outcome.
	if decision, handled := ai.PlanTerminalPermission(ts.planMode, ai.PermissionRequest{
		Tool: in.Tool, Input: in.Input, ToolUseID: in.ToolUseID, SessionID: sessionID,
	}); handled {
		return canUseToolResult{Allow: decision.Allow, Message: decision.Message}, nil
	}

	if ts.canUseTool == nil {
		return canUseToolResult{Allow: true, UpdatedInput: in.Input}, nil
	}

	p.deliver(ts, ai.Event{
		Kind:       ai.EventPermission,
		Tool:       in.Tool,
		Input:      in.Input,
		ToolCallID: in.ToolUseID,
		SessionID:  sessionID,
		Model:      p.model,
	})

	decision, err := ts.canUseTool(ts.ctx, ai.PermissionRequest{
		Tool:      in.Tool,
		Input:     in.Input,
		ToolUseID: in.ToolUseID,
		SessionID: sessionID,
	})
	if err != nil {
		return canUseToolResult{Allow: false, Message: err.Error()}, nil
	}
	return canUseToolResult{
		Allow:        decision.Allow,
		Message:      decision.Message,
		UpdatedInput: decision.UpdatedInput,
	}, nil
}

// deliver forwards ev to the active turn without blocking past the turn's life:
// it returns once enqueued, the turn exits, or the provider closes.
func (p *Provider) deliver(ts *turnState, ev ai.Event) {
	select {
	case ts.inbox <- ev:
	case <-ts.quit:
	case <-p.baseCtx.Done():
	}
}

func (p *Provider) Steer(ctx context.Context, req api.Spec) error {
	p.activeMu.Lock()
	ts := p.active
	p.activeMu.Unlock()
	if ts == nil || !ts.addPrompt() {
		return fmt.Errorf("claude-agent: no active turn to steer")
	}
	params, err := buildPromptParams(req)
	if err != nil {
		ts.completePrompt()
		return err
	}
	if _, err := p.rpc.Call(ctx, methodPrompt, params); err != nil {
		ts.completePrompt()
		return fmt.Errorf("claude-agent steer failed: %w", err)
	}
	return nil
}

func (p *Provider) Interrupt(ctx context.Context) error {
	if p.rpc == nil {
		return fmt.Errorf("claude-agent: provider not started")
	}
	p.activeMu.Lock()
	ts := p.active
	p.activeMu.Unlock()
	if ts == nil {
		return fmt.Errorf("claude-agent: no active turn to interrupt")
	}
	ts.interrupting.Store(true)
	if _, err := p.rpc.Call(ctx, methodInterrupt, nil); err != nil {
		ts.interrupting.Store(false)
		return fmt.Errorf("claude-agent interrupt failed: %w", err)
	}
	return nil
}

func (p *Provider) interrupt() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.Interrupt(ctx)
}

func (p *Provider) setActive(ts *turnState) {
	p.activeMu.Lock()
	p.active = ts
	p.activeMu.Unlock()
}

func (p *Provider) clearActive() {
	p.activeMu.Lock()
	p.active = nil
	p.activeMu.Unlock()
}

func (p *Provider) rememberSession(id string) {
	p.sessMu.Lock()
	p.sessionID = id
	p.sessMu.Unlock()
}

func (ts *turnState) addPrompt() bool {
	ts.promptMu.Lock()
	defer ts.promptMu.Unlock()
	if ts.ended {
		return false
	}
	ts.pending++
	return true
}

func (ts *turnState) completePrompt() {
	ts.promptMu.Lock()
	defer ts.promptMu.Unlock()
	if ts.ended {
		return
	}
	ts.pending--
	if ts.pending > 0 {
		return
	}
	ts.ended = true
	close(ts.term)
}

// emit sends ev on events, honouring ctx cancellation. Returns false if ctx was
// cancelled before the send completed.
func emit(ctx context.Context, events chan ai.Event, ev ai.Event) bool {
	select {
	case events <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// drainInbox flushes any events the handler already queued (e.g. the terminal
// result enqueued just before term closed) without blocking.
func (p *Provider) drainInbox(ctx context.Context, inbox chan ai.Event, events chan ai.Event) {
	for {
		select {
		case ev := <-inbox:
			if !emit(ctx, events, p.withCapturedOutput(ev)) {
				return
			}
		default:
			return
		}
	}
}
