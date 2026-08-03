package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/flanksource/captain/pkg/ai"
)

type turnState struct {
	ch         chan ai.Event
	usage      *ai.Usage
	model      string
	streamed   map[string]string
	toolOutput map[string]string

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
