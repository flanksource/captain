package provider

import (
	"context"
	"encoding/json"
	"fmt"
	osexec "os/exec"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	"github.com/flanksource/clicky/exec"
	"github.com/flanksource/commons/logger"
)

// CodexAppServer drives `codex app-server` over JSON-RPC 2.0 stdio (the codex
// app-server v2 schema: slash-delimited methods, camelCase params), replacing
// the one-shot `codex exec --json` path in codex_cli.go. Each Execute runs one
// turn (thread/start|resume + turn/start) and drains server notifications until
// turn/completed; the notification→ai.Event mapping lives in the pure,
// unit-testable mapAppServerNotification.
type CodexAppServer struct {
	model string

	turnMu sync.Mutex // serializes turns; held by ExecuteStream, freed by its driver

	mu      sync.Mutex // guards the rpc/supervisor handles + the active turn
	sup     *exec.SupervisedProcess
	rpc     *jsonrpc.Client
	rpcDone chan struct{} // closed by the rpc Run goroutine when the child exits
	active  *turnState
}

const (
	appServerStartTimeout = 30 * time.Second
	appServerInterruptTTL = 2 * time.Second
)

// NewCodexAppServer builds a codex app-server provider. The supervised process
// is started lazily on the first ExecuteStream.
func NewCodexAppServer(model string) (*CodexAppServer, error) {
	if model == "" {
		model = CodexCLIDefaultModel
	}
	return &CodexAppServer{model: model}, nil
}

func (c *CodexAppServer) GetModel() string       { return c.model }
func (c *CodexAppServer) GetBackend() ai.Backend { return ai.BackendCodexCLI }

// Execute drains the streaming output into a buffered ai.Response.
func (c *CodexAppServer) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()
	events, err := c.ExecuteStream(ctx, req)
	if err != nil {
		return nil, err
	}
	return CoalesceStream(ctx, c.model, events, start)
}

// ExecuteStream runs a single turn; the channel closes on turn/completed, a
// fatal error, ctx cancellation, or a child crash.
func (c *CodexAppServer) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	if req.StructuredOutput != nil {
		return nil, fmt.Errorf("codex app-server does not support StructuredOutput; use a direct API backend")
	}

	c.turnMu.Lock()
	if err := c.ensureStarted(ctx); err != nil {
		c.turnMu.Unlock()
		return nil, err
	}
	c.mu.Lock()
	rpcDone := c.rpcDone
	c.mu.Unlock()

	ts := &turnState{
		ch:       make(chan ai.Event, 16),
		usage:    &ai.Usage{},
		model:    c.model,
		streamed: map[string]bool{},
		terminal: make(chan struct{}),
	}
	c.setActive(ts)

	go c.driveTurn(ctx, req, ts, rpcDone)
	return ts.ch, nil
}

// driveTurn issues thread/start|resume + turn/start, then waits for completion,
// ctx cancellation, or a child crash.
func (c *CodexAppServer) driveTurn(ctx context.Context, req ai.Request, ts *turnState, rpcDone <-chan struct{}) {
	defer c.turnMu.Unlock()

	threadID, err := c.startThread(ctx, req)
	if err != nil {
		c.failTurn(ts, err)
		return
	}
	turnID, err := c.startTurn(ctx, req, threadID)
	if err != nil {
		c.failTurn(ts, err)
		return
	}

	select {
	case <-ts.terminal:
	case <-ctx.Done():
		c.interrupt(threadID, turnID)
		c.teardown(true) // ctx cancel tears the server down (best-effort)
		ts.send(ai.Event{Kind: ai.EventError, Error: "codex app-server: turn cancelled", Model: c.model})
	case <-rpcDone:
		c.teardown(false) // child crashed; surface, never silently retry
		ts.send(ai.Event{Kind: ai.EventError, Error: "codex app-server exited unexpectedly", Model: c.model})
	}
	c.clearActive(ts)
	ts.finish()
}

func (c *CodexAppServer) failTurn(ts *turnState, err error) {
	c.clearActive(ts)
	ts.send(ai.Event{Kind: ai.EventError, Error: extractCodexErrorText(err.Error()), Model: c.model})
	ts.finish()
}

func (c *CodexAppServer) setActive(ts *turnState) { c.mu.Lock(); c.active = ts; c.mu.Unlock() }
func (c *CodexAppServer) currentTurn() *turnState { c.mu.Lock(); defer c.mu.Unlock(); return c.active }
func (c *CodexAppServer) client() *jsonrpc.Client { c.mu.Lock(); defer c.mu.Unlock(); return c.rpc }

func (c *CodexAppServer) clearActive(ts *turnState) {
	c.mu.Lock()
	if c.active == ts {
		c.active = nil
	}
	c.mu.Unlock()
}

// ensureStarted lazily spawns the supervised server, binds the JSON-RPC client
// to its stdio, and performs the initialize handshake (serialized by turnMu).
func (c *CodexAppServer) ensureStarted(ctx context.Context) error {
	if c.client() != nil {
		return nil
	}
	if _, err := osexec.LookPath("codex"); err != nil {
		return fmt.Errorf("%w: %v", ai.ErrCLINotFound, err)
	}

	ready := make(chan error, 1)
	sup := exec.NewExec("codex", "app-server").WithStdioPipe().Supervise(exec.SuperviseOptions{
		// No restart: a crash surfaces as EventError, never a silent retry.
		RestartPolicy: exec.RestartNo,
		OnStarted: func(p *exec.Process) {
			rpc := jsonrpc.New(p.Stdin(), p.StdoutReader(), true, jsonrpc.Handlers{
				OnNotification: c.handleNotification,
				OnRequest:      c.handleApproval,
			})
			rpcDone := make(chan struct{})
			go func() { _ = rpc.Run(context.Background()); close(rpcDone) }()
			c.mu.Lock()
			c.rpc, c.rpcDone = rpc, rpcDone
			c.mu.Unlock()
			// Handshake must not block the supervise loop (see OnStarted doc).
			go func() { ready <- c.handshake(ctx, rpc) }()
		},
	})
	c.mu.Lock()
	c.sup = sup
	c.mu.Unlock()
	logger.Debugf("[codex-appserver] starting codex app-server")
	sup.Start()

	select {
	case err := <-ready:
		if err != nil {
			c.teardown(true)
			return err
		}
		return nil
	case <-ctx.Done():
		c.teardown(true)
		return ctx.Err()
	case <-time.After(appServerStartTimeout):
		c.teardown(true)
		return fmt.Errorf("codex app-server: timed out waiting for initialize handshake")
	}
}

// handshake performs the required initialize → initialized exchange (any request
// before it errors "Not initialized" server-side).
func (c *CodexAppServer) handshake(ctx context.Context, rpc *jsonrpc.Client) error {
	params := map[string]any{"clientInfo": map[string]string{"name": "captain", "version": "dev"}}
	if _, err := rpc.Call(ctx, "initialize", params); err != nil {
		return fmt.Errorf("codex app-server initialize: %w", err)
	}
	if err := rpc.Notify("initialized", nil); err != nil {
		return fmt.Errorf("codex app-server initialized notify: %w", err)
	}
	return nil
}

// teardown clears the rpc/supervisor handles so the next ExecuteStream lazily
// restarts; it stops the process only when stop is true (a crash already ended it).
func (c *CodexAppServer) teardown(stop bool) {
	c.mu.Lock()
	sup := c.sup
	c.sup, c.rpc, c.rpcDone = nil, nil, nil
	c.mu.Unlock()
	if stop && sup != nil {
		sup.Stop()
	}
}

func (c *CodexAppServer) startThread(ctx context.Context, req ai.Request) (string, error) {
	rpc := c.client()
	if rpc == nil {
		return "", fmt.Errorf("codex app-server: not started")
	}
	if req.SessionID != "" {
		raw, err := rpc.Call(ctx, "thread/resume", buildResumeParams(req))
		if err != nil {
			return "", err
		}
		return firstNonEmpty(parseAppServerNotif(raw).threadID(), req.SessionID), nil
	}
	raw, err := rpc.Call(ctx, "thread/start", buildThreadStartParams(c.model, req))
	if err != nil {
		return "", err
	}
	return parseAppServerNotif(raw).threadID(), nil
}

func (c *CodexAppServer) startTurn(ctx context.Context, req ai.Request, threadID string) (string, error) {
	rpc := c.client()
	if rpc == nil {
		return "", fmt.Errorf("codex app-server: not started")
	}
	raw, err := rpc.Call(ctx, "turn/start", buildTurnStartParams(c.model, req, threadID))
	if err != nil {
		return "", err
	}
	if t := parseAppServerNotif(raw).Turn; t != nil {
		return t.ID, nil
	}
	return "", nil
}

// interrupt best-effort cancels the in-flight turn using a fresh short context
// because the caller's ctx is already cancelled.
func (c *CodexAppServer) interrupt(threadID, turnID string) {
	rpc := c.client()
	if rpc == nil || threadID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), appServerInterruptTTL)
	defer cancel()
	_, _ = rpc.Call(ctx, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID})
}

// handleNotification routes one notification to the active turn. It runs on the
// rpc Run goroutine (notifications dispatch sequentially), so the per-turn
// dedup/usage state needs no extra locking.
func (c *CodexAppServer) handleNotification(method string, params json.RawMessage) {
	ts := c.currentTurn()
	if ts == nil {
		return
	}
	switch method {
	case "item/agentMessage/delta":
		if id := parseAppServerNotif(params).ItemID; id != "" {
			ts.streamed[id] = true
		}
	case "item/completed":
		// The final agent message repeats text already streamed via deltas; drop
		// it so CoalesceStream / renderers don't double-count.
		if appServerStreamedAgentMessage(params, ts.streamed) {
			return
		}
	}
	if ev, ok := mapAppServerNotification(method, params, ts.model, ts.usage); ok {
		ts.send(ev)
	}
	if method == "turn/completed" || appServerErrorIsFatal(method, params) {
		c.clearActive(ts)
		ts.signalTerminal()
	}
}

// handleApproval auto-approves server→client approval requests, mirroring the
// `--dangerously-bypass-approvals` default of the exec path. Decision shapes
// differ per method (see the *ApprovalResponse schemas).
func (c *CodexAppServer) handleApproval(method string, _ json.RawMessage) (any, *jsonrpc.RPCError) {
	switch method {
	case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
		return map[string]string{"decision": "accept"}, nil
	case "item/permissions/requestApproval":
		return map[string]any{"permissions": map[string]any{}, "scope": "turn"}, nil
	case "item/tool/requestUserInput":
		return map[string]any{}, nil
	default: // execCommandApproval, applyPatchApproval, unknown
		return map[string]string{"decision": "approved"}, nil
	}
}

// --- turn state ------------------------------------------------------------

// turnState is the routing target for one turn's server notifications. send and
// finish are guarded so a late notification can never send on a closed channel;
// terminal doubles as the "stop sending" signal that unblocks a blocked send.
type turnState struct {
	ch       chan ai.Event
	usage    *ai.Usage
	model    string
	streamed map[string]bool // agent-message item IDs already streamed via deltas

	terminal chan struct{} // closed when the turn is over (by codex or by finish)
	termOnce sync.Once
	sendMu   sync.Mutex
	closed   bool
}

func (ts *turnState) signalTerminal() { ts.termOnce.Do(func() { close(ts.terminal) }) }

func (ts *turnState) send(ev ai.Event) {
	ts.sendMu.Lock()
	defer ts.sendMu.Unlock()
	if ts.closed {
		return
	}
	select {
	case ts.ch <- ev:
	case <-ts.terminal:
	}
}

func (ts *turnState) finish() {
	ts.signalTerminal() // unblock a blocked send before locking
	ts.sendMu.Lock()
	defer ts.sendMu.Unlock()
	if ts.closed {
		return
	}
	ts.closed = true
	close(ts.ch)
}

// compile-time assertion: the provider satisfies the streaming interface.
// The pure mapping, parse structs, and request-param builders live in the
// sibling codex_appserver_protocol.go (same package).
var _ ai.StreamingProvider = (*CodexAppServer)(nil)
