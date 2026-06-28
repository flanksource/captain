package claudeagent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/flanksource/captain/pkg/ai"
)

// turnState carries the channels a single in-flight turn uses to receive mapped
// events from the JSON-RPC notification handler. inbox is the handler->turn
// path; term is closed by the handler on a terminal notification; quit is closed
// by the turn goroutine on exit so a late handler send does not block.
type turnState struct {
	inbox chan ai.Event
	term  chan struct{}
	quit  chan struct{}
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
		inbox: make(chan ai.Event, 16),
		term:  make(chan struct{}),
		quit:  make(chan struct{}),
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
