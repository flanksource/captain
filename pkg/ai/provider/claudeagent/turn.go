package claudeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
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
}

type promptParams struct {
	Text string `json:"text"`
}

func composePrompt(req ai.Request) string {
	return req.Prompt
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
		canUseTool: req.CanUseTool,
	}
	p.setActive(ts)
	defer func() {
		close(ts.quit)
		p.clearActive()
	}()

	if _, err := p.rpc.Call(ctx, methodPrompt, promptParams{Text: composePrompt(req)}); err != nil {
		emit(ctx, events, ai.Event{Kind: ai.EventError, Error: fmt.Sprintf("claude-agent prompt failed: %v", err), Model: p.model})
		return
	}

	for {
		select {
		case ev := <-ts.inbox:
			if !emit(ctx, events, ev) {
				return
			}
		case <-ts.term:
			drainInbox(ctx, ts.inbox, events)
			return
		case <-ctx.Done():
			p.interrupt()
			emit(context.Background(), events, ai.Event{Kind: ai.EventError, Error: "claude-agent: context cancelled", Model: p.model})
			return
		case <-p.baseCtx.Done():
			emit(context.Background(), events, ai.Event{Kind: ai.EventError, Error: "claude-agent: provider closed", Model: p.model})
			return
		case <-p.procExited:
			emit(context.Background(), events, ai.Event{Kind: ai.EventError, Error: "claude-agent: process exited mid-turn", Model: p.model})
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

	if ok {
		select {
		case ts.inbox <- ev:
		case <-ts.quit:
		case <-p.baseCtx.Done():
		}
	}
	if method == notifyTurnDone || method == notifyTurnError {
		select {
		case <-ts.term:
		default:
			close(ts.term)
		}
	}
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
	if ts == nil || ts.canUseTool == nil {
		return canUseToolResult{Allow: true, UpdatedInput: in.Input}, nil
	}

	p.sessMu.Lock()
	sessionID := p.sessionID
	p.sessMu.Unlock()

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

func (p *Provider) interrupt() {
	if p.rpc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = p.rpc.Call(ctx, methodInterrupt, nil)
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
func drainInbox(ctx context.Context, inbox chan ai.Event, events chan ai.Event) {
	for {
		select {
		case ev := <-inbox:
			if !emit(ctx, events, ev) {
				return
			}
		default:
			return
		}
	}
}
