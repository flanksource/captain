package provider

import (
	"context"
	"encoding/json"
	"fmt"
	osexec "os/exec"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/callertools"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	aitools "github.com/flanksource/captain/pkg/ai/tools"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/exec"
	"github.com/flanksource/commons/logger"
)

// log is the package-scoped logger for AI providers. Its level follows
// -v/--log-level and can be tuned with -Plog.level.ai=debug.
var log = logger.GetLogger("ai")

// CodexAppServer drives `codex app-server` over JSON-RPC 2.0 stdio (the codex
// app-server v2 schema: slash-delimited methods, camelCase params), replacing
// the one-shot `codex exec --json` path in codex_cli.go. Each Execute runs one
// turn (thread/start|resume + turn/start) and drains server notifications until
// turn/completed; the notification→ai.Event mapping lives in the pure,
// unit-testable mapAppServerNotification.
type CodexAppServer struct {
	model string
	cfg   ai.Config

	turnMu sync.Mutex // serializes turns; held by ExecuteStream, freed by its driver

	mu       sync.Mutex // guards the rpc/supervisor handles + the active turn
	sup      *exec.SupervisedProcess
	rpc      *jsonrpc.Client
	rpcDone  chan struct{} // closed by the rpc Run goroutine when the child exits
	active   *turnState
	threadID string

	callerToolsMu      sync.Mutex
	callerToolsRuntime *callertools.Runtime
	callerTools        *api.CallerToolEndpoint
}

const (
	appServerStartTimeout = 30 * time.Second
	appServerInterruptTTL = 2 * time.Second
)

// NewCodexAppServer builds a codex app-server provider. The supervised process
// is started lazily on the first ExecuteStream.
func NewCodexAppServer(cfg ai.Config) (*CodexAppServer, error) {
	model := cfg.Model.Name
	if model == "" {
		model = CodexCLIDefaultModel
	}
	model = ai.NormalizeModelForBackend(ai.BackendCodexAgent, model)
	provider := &CodexAppServer{model: model, cfg: cfg}
	if cfg.CallerTools != nil {
		endpoint := *cfg.CallerTools
		endpoint.Headers = cloneStringMap(cfg.CallerTools.Headers)
		provider.callerTools = &endpoint
	}
	return provider, nil
}

func (c *CodexAppServer) GetModel() string          { return c.model }
func (c *CodexAppServer) GetBackend() ai.Backend    { return ai.BackendCodexAgent }
func (c *CodexAppServer) SupportsCallerTools() bool { return true }

var _ api.ToolCapableProvider = (*CodexAppServer)(nil)

// Execute drains the streaming output into a buffered ai.Response. When the
// request carries a structured-output schema, the final agent message's JSON is
// unmarshalled into req.Prompt.Schema and surfaced as StructuredData (mirroring
// the genkit and claude-agent providers).
func (c *CodexAppServer) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()
	events, err := c.ExecuteStream(ctx, req)
	if err != nil {
		return nil, err
	}
	resp, err := CoalesceStreamForBackend(ctx, ai.BackendCodexAgent, c.model, events, start)
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
	} else if len(req.Prompt.SchemaJSON) > 0 {
		// A pre-built JSON schema has no Go target to bind into; leave the raw
		// structured JSON on Text for tolerant decoders.
		if raw, ok := resp.StructuredData.(json.RawMessage); ok && len(raw) > 0 {
			resp.Text = string(raw)
		}
	}
	return resp, nil
}

// codexOutputSchema derives the JSON schema codex should constrain the final
// message to (a reflected Go struct or a verbatim Prompt.SchemaJSON), or nil for
// a text-mode request. A non-struct target fails loudly rather than silently
// dropping the schema.
func codexOutputSchema(req ai.Request) (json.RawMessage, error) {
	schema, err := ai.SchemaJSONForBackend(ai.BackendCodexAgent, req.Prompt)
	if err != nil {
		return nil, fmt.Errorf("codex app-server: cannot derive structured-output schema: %w", err)
	}
	return schema, nil
}

// ExecuteStream runs a single turn; the channel closes on turn/completed, a
// fatal error, ctx cancellation, or a child crash.
func (c *CodexAppServer) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	schema, err := codexOutputSchema(req)
	if err != nil {
		return nil, err
	}

	if err := c.prepareCallerTools(req); err != nil {
		return nil, err
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
		ch:           make(chan ai.Event, 16),
		usage:        &ai.Usage{},
		model:        c.model,
		streamed:     map[string]string{},
		toolOutput:   map[string]string{},
		terminal:     make(chan struct{}),
		started:      make(chan struct{}),
		outputSchema: schema,
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
	turnID, err := c.startTurn(ctx, req, threadID, ts.outputSchema)
	if err != nil {
		c.failTurn(ts, err)
		return
	}
	ts.setIDs(threadID, turnID)

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
	var process *exec.Process
	sup := newCodexAppServerProcess(c.cfg).WithStdioPipe().Supervise(exec.SuperviseOptions{
		// No restart: a crash surfaces as EventError, never a silent retry.
		RestartPolicy: exec.RestartNo,
		OnStarted: func(p *exec.Process) {
			process = p
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
	log.Debugf("[codex-appserver] starting codex app-server")
	sup.Start()

	select {
	case err := <-ready:
		if err != nil {
			if process != nil {
				err = appServerProcessError(err, process.GetStderr())
			}
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
	c.sup, c.rpc, c.rpcDone, c.threadID = nil, nil, nil, ""
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
	c.mu.Lock()
	threadID := c.threadID
	c.mu.Unlock()
	if threadID != "" {
		return threadID, nil
	}
	if req.SessionID != "" {
		raw, err := rpc.Call(ctx, "thread/resume", buildResumeParams(req, c.callerTools))
		if err != nil {
			return "", err
		}
		threadID = firstNonEmpty(parseAppServerNotif(raw).threadID(), req.SessionID)
		c.rememberThread(threadID)
		return threadID, nil
	}
	raw, err := rpc.Call(ctx, "thread/start", buildThreadStartParams(c.model, req, c.callerTools))
	if err != nil {
		return "", err
	}
	threadID = parseAppServerNotif(raw).threadID()
	if threadID == "" {
		return "", fmt.Errorf("codex app-server: thread/start returned no thread id")
	}
	c.rememberThread(threadID)
	return threadID, nil
}

func (c *CodexAppServer) rememberThread(threadID string) {
	c.mu.Lock()
	c.threadID = threadID
	c.mu.Unlock()
}

func (c *CodexAppServer) startTurn(ctx context.Context, req ai.Request, threadID string, outputSchema json.RawMessage) (string, error) {
	rpc := c.client()
	if rpc == nil {
		return "", fmt.Errorf("codex app-server: not started")
	}
	params, err := buildTurnStartParams(c.model, req, threadID, outputSchema)
	if err != nil {
		return "", err
	}
	raw, err := rpc.Call(ctx, "turn/start", params)
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

func (c *CodexAppServer) Interrupt(ctx context.Context) error {
	ts := c.currentTurn()
	if ts == nil {
		return fmt.Errorf("codex app-server: no active turn to interrupt")
	}
	threadID, turnID, err := ts.waitIDs(ctx)
	if err != nil {
		return err
	}
	rpc := c.client()
	if rpc == nil {
		return fmt.Errorf("codex app-server: not started")
	}
	if _, err := rpc.Call(ctx, "turn/interrupt", map[string]string{"threadId": threadID, "turnId": turnID}); err != nil {
		return fmt.Errorf("codex app-server interrupt failed: %w", err)
	}
	return nil
}

func (c *CodexAppServer) Close() error {
	c.teardown(true)
	if c.callerToolsRuntime != nil {
		return c.callerToolsRuntime.Close()
	}
	return nil
}

func (c *CodexAppServer) prepareCallerTools(req ai.Request) error {
	c.callerToolsMu.Lock()
	defer c.callerToolsMu.Unlock()
	if c.callerTools != nil {
		if req.Permissions.MCP.Disabled {
			return fmt.Errorf("codex app-server: caller tools require MCP but MCP is disabled")
		}
		return c.callerTools.Validate()
	}
	if len(c.cfg.Tools) == 0 {
		return nil
	}
	definitions, err := aitools.ResolveDefinitions(c.cfg.Tools, req.ToolPreferences)
	if err != nil {
		return fmt.Errorf("codex app-server caller tools: %w", err)
	}
	if len(definitions) == 0 {
		return nil
	}
	if req.Permissions.MCP.Disabled {
		return fmt.Errorf("codex app-server: caller tools require MCP but MCP is disabled")
	}
	runtime, err := callertools.New(callertools.Options{
		Definitions: definitions, CanUseTool: c.cfg.CanUseTool,
		SessionID: firstNonEmpty(c.cfg.CaptainSessionID, req.SessionID, c.cfg.SessionID),
	})
	if err != nil {
		return fmt.Errorf("start codex app-server caller tools: %w", err)
	}
	endpoint := runtime.Endpoint()
	c.callerToolsRuntime = runtime
	c.callerTools = &endpoint
	return nil
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

// handleNotification routes one notification to the active turn. It runs on the
// rpc Run goroutine (notifications dispatch sequentially), so the per-turn
// dedup/usage state needs no extra locking.
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
	if ev, ok := mapAppServerNotification(method, params, ctx); ok {
		if ev.Kind == ai.EventResult && len(ts.outputSchema) > 0 {
			ev.StructuredData = json.RawMessage(ts.lastAgentMessage)
		}
		ts.send(ev)
	}
	if method == "turn/completed" || appServerErrorIsFatal(method, params) {
		c.clearActive(ts)
		ts.signalTerminal()
	}
}

// --- turn state ------------------------------------------------------------

// turnState is the routing target for one turn's server notifications. send and
// finish are guarded so a late notification can never send on a closed channel;
// terminal doubles as the "stop sending" signal that unblocks a blocked send.
// compile-time assertion: the provider satisfies the streaming interface.
// The pure mapping, parse structs, and request-param builders live in the
// sibling codex_appserver_protocol.go (same package).
var _ ai.StreamingProvider = (*CodexAppServer)(nil)
