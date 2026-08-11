package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/claude"
)

type turnState struct {
	ch                 chan ai.Event
	usage              *ai.Usage
	model              string
	streamed           map[string]string
	toolOutput         map[string]string
	completeToolOutput map[string]string
	pendingToolResults []ai.Event
	toolResultsMu      sync.Mutex
	toolResultsClosed  bool

	outputSchema     json.RawMessage
	lastAgentMessage string
	terminal         chan struct{}
	termOnce         sync.Once
	started          chan struct{}
	startOnce        sync.Once
	idMu             sync.Mutex
	threadID         string
	turnID           string
	sendMu           sync.Mutex
	closed           bool
}

func (ts *turnState) signalTerminal() { ts.termOnce.Do(func() { close(ts.terminal) }) }

func (ts *turnState) setIDs(threadID, turnID string) {
	ts.idMu.Lock()
	ts.threadID = threadID
	ts.turnID = turnID
	ts.idMu.Unlock()
	ts.signalStarted()
}

func (ts *turnState) signalStarted() {
	if ts.started != nil {
		ts.startOnce.Do(func() { close(ts.started) })
	}
}

func (ts *turnState) waitIDs(ctx context.Context) (string, string, error) {
	if ts.started == nil {
		return "", "", fmt.Errorf("codex app-server: turn identifiers unavailable")
	}
	select {
	case <-ts.started:
	case <-ts.terminal:
		return "", "", fmt.Errorf("codex app-server: turn ended before interrupt was ready")
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
	ts.idMu.Lock()
	defer ts.idMu.Unlock()
	if ts.threadID == "" {
		return "", "", fmt.Errorf("codex app-server: turn identifiers unavailable")
	}
	return ts.threadID, ts.turnID, nil
}

func (ts *turnState) send(event ai.Event) {
	ts.sendMu.Lock()
	defer ts.sendMu.Unlock()
	if ts.closed {
		return
	}
	select {
	case ts.ch <- event:
	case <-ts.terminal:
	}
}

func (ts *turnState) finish() {
	ts.signalStarted()
	ts.signalTerminal()
	ts.sendMu.Lock()
	defer ts.sendMu.Unlock()
	if ts.closed {
		return
	}
	ts.closed = true
	close(ts.ch)
}

func (ts *turnState) queueToolResult(event ai.Event) {
	ts.toolResultsMu.Lock()
	defer ts.toolResultsMu.Unlock()
	if ts.toolResultsClosed {
		return
	}
	if output, ok := ts.completeToolOutput[event.ToolCallID]; ok {
		delete(ts.completeToolOutput, event.ToolCallID)
		ts.send(withToolResultText(event, output))
		return
	}
	ts.pendingToolResults = append(ts.pendingToolResults, event)
}

func (ts *turnState) receiveCompleteToolOutput(toolCallID, output string) {
	ts.toolResultsMu.Lock()
	defer ts.toolResultsMu.Unlock()
	if ts.toolResultsClosed {
		return
	}
	for i, event := range ts.pendingToolResults {
		if event.ToolCallID != toolCallID {
			continue
		}
		ts.pendingToolResults = append(ts.pendingToolResults[:i], ts.pendingToolResults[i+1:]...)
		ts.send(withToolResultText(event, output))
		return
	}
	if ts.completeToolOutput == nil {
		ts.completeToolOutput = map[string]string{}
	}
	ts.completeToolOutput[toolCallID] = output
}

func (ts *turnState) flushToolResults() {
	ts.toolResultsMu.Lock()
	defer ts.toolResultsMu.Unlock()
	if ts.toolResultsClosed {
		return
	}
	ts.toolResultsClosed = true
	for _, event := range ts.pendingToolResults {
		ts.send(event)
	}
	ts.pendingToolResults = nil
	ts.completeToolOutput = nil
}

func withToolResultText(event ai.Event, text string) ai.Event {
	event.Text = text
	if raw, ok := event.Raw.(claude.ToolUse); ok {
		raw.Response = text
		event.Raw = raw
	}
	return event
}

// handleNotification routes one notification to the active turn. It runs on the
// rpc Run goroutine (notifications dispatch sequentially). Pending tool results
// also synchronize with driveTurn's cancellation and process-exit paths.
func (c *CodexAppServer) handleNotification(method string, params json.RawMessage) {
	ts := c.currentTurn()
	if ts == nil {
		return
	}
	ctx := appServerEventContext{Model: ts.model, Usage: ts.usage}
	switch method {
	case "item/agentMessage/delta":
		notification := parseAppServerNotif(params)
		if notification.ItemID != "" {
			ts.streamed[notification.ItemID] += notification.Delta
		}
		if len(ts.outputSchema) > 0 {
			return
		}
	case "rawResponseItem/completed":
		if toolCallID, output, ok := appServerRawCommandOutput(params); ok {
			ts.receiveCompleteToolOutput(toolCallID, output)
		}
		return
	case "item/commandExecution/outputDelta":
		n := parseAppServerNotif(params)
		if n.ItemID != "" && n.Delta != "" {
			ts.toolOutput[n.ItemID] += n.Delta
		}
		return
	case "item/completed":
		// The completed agent message carries the full text (structured runs use it
		// as the validated JSON result). Capture it, then drop the duplicate so
		// CoalesceStream / renderers don't double-count already-streamed deltas.
		if it := parseAppServerNotif(params).Item; it != nil && it.Type == "agentMessage" {
			ts.lastAgentMessage = it.Text
			if len(ts.outputSchema) > 0 {
				return
			}
		}
		remainder, streamed, err := appServerAgentMessageRemainder(params, ts.streamed)
		if err != nil {
			ts.send(ai.Event{Kind: ai.EventError, Error: err.Error(), Model: ts.model})
			return
		}
		if streamed {
			if remainder != "" {
				ts.send(ai.Event{Kind: ai.EventText, Text: remainder, Model: ts.model})
			}
			return
		}
		if it := parseAppServerNotif(params).Item; it != nil {
			ctx.ToolOutput = ts.toolOutput[it.ID]
			delete(ts.toolOutput, it.ID)
		}
	}
	terminal := method == "turn/completed" || appServerErrorIsFatal(method, params)
	if terminal {
		ts.flushToolResults()
	}
	if ev, ok := mapAppServerNotification(method, params, ctx); ok {
		if ev.Kind == ai.EventResult && len(ts.outputSchema) > 0 {
			ev.StructuredData = json.RawMessage(ts.lastAgentMessage)
		}
		if method == "item/completed" && ev.Kind == ai.EventToolResult {
			it := parseAppServerNotif(params).Item
			if it != nil && it.Type == "commandExecution" {
				ts.queueToolResult(ev)
				return
			}
		}
		ts.send(ev)
	}
	if terminal {
		c.clearActive(ts)
		ts.signalTerminal()
	}
}
