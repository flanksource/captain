// Package claudeagent implements captain's claude-agent provider: the Claude
// Agent SDK run as a long-lived clicky-supervised TypeScript process, spoken to
// over JSON-RPC stdio. It replaces the one-shot `claude -p` claude_cli provider
// for agentic, multi-turn, tool-using runs.
//
// One supervised tsx process per Provider hosts a single SDK query() session.
// It is started lazily on the first ExecuteStream (which provisions the agent
// dir, installs deps, binds a jsonrpc.Client to the child's stdio, and sends the
// initialize handshake). Each ExecuteStream pushes one user turn over the
// JSON-RPC `prompt` method and streams the resulting notifications back as
// ai.Events; turns are serialized so the single SDK session stays consistent.
//
// pkg/ai/provider/init.go registers claudeagent.New for ai.BackendClaudeAgent.
package claudeagent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/provider/jsonrpc"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/clicky/exec"
	"github.com/flanksource/commons/logger"
)

// log is the package-scoped logger for AI providers. Its level follows
// -v/--log-level and can be tuned with -Plog.level.ai=debug.
var log = logger.GetLogger("ai")

// JSON-RPC request methods sent to agent.ts.
const (
	methodInitialize = "initialize"
	methodPrompt     = "prompt"
	methodInterrupt  = "interrupt"
	methodShutdown   = "shutdown"
)

// methodCanUseTool is the server→client request agent.ts sends to broker a
// tool-permission decision (the stream-json control protocol's can_use_tool).
const methodCanUseTool = "can_use_tool"

const (
	defaultModel = "claude-sonnet-5"
	// initTimeout bounds the initialize handshake (after provisioning). The npm
	// install / tsx cold start happen synchronously before this window.
	initTimeout = 2 * time.Minute
	// initCallTimeout bounds the single initialize Call once stdio is bound.
	initCallTimeout = 90 * time.Second
)

// safeEditAllowlist is the curated allowlist applied by --edit. It is kept local
// so this package never imports pkg/ai/provider (which imports claudeagent back).
var safeEditAllowlist = []string{"Read", "Edit", "Write", "Glob", "Grep"}

// Provider must satisfy ai.StreamingProvider so init.go can register New for
// ai.BackendClaudeAgent.
var _ ai.StreamingProvider = (*Provider)(nil)

// newAgentProcess builds the supervised child command. It is a package var so
// tests can substitute a fake JSON-RPC server without npm or a claude binary.
var newAgentProcess = func(*Provider) (*exec.Process, error) {
	agentDir, err := prepareAgentDir()
	if err != nil {
		return nil, err
	}
	if err := ensureDependencies(agentDir); err != nil {
		return nil, err
	}
	tsxPath, err := findTsx(agentDir)
	if err != nil {
		return nil, err
	}
	agentTSPath := agentDir + string(os.PathSeparator) + "agent.ts"
	log.Debugf("[claude-agent] exec: %s %s (cwd=%s)", tsxPath, agentTSPath, agentDir)
	return exec.NewExec(tsxPath, agentTSPath).
		WithCwd(agentDir).
		WithStdioPipe().
		WithEnv(nestingEnvOverrides(os.Environ())), nil
}

// Provider drives a supervised Claude Agent SDK process over JSON-RPC.
type Provider struct {
	model string
	cfg   ai.Config

	baseCtx    context.Context
	baseCancel context.CancelFunc

	startOnce sync.Once
	initMu    sync.Mutex
	initDone  chan struct{}
	initErr   error

	procExited     chan struct{}
	procExitedOnce sync.Once

	sup *exec.SupervisedProcess
	rpc *jsonrpc.Client

	turnMu sync.Mutex // serializes turns (single SDK session)

	activeMu sync.Mutex
	active   *turnState

	sessMu    sync.Mutex
	sessionID string

	// sessionSchema is the JSON schema the SDK query() session was initialized
	// with (nil = text mode). The SDK's outputFormat is a session-level option,
	// so it is pinned from the first turn and every later turn must match it.
	sessionSchemaOnce sync.Once
	sessionSchema     json.RawMessage
}

// New builds a claude-agent provider. The supervised process is started lazily
// on the first ExecuteStream.
func New(cfg ai.Config) (*Provider, error) {
	model := cfg.Model.Name
	if model == "" {
		model = defaultModel
	}
	model = ai.NormalizeModelForBackend(ai.BackendClaudeAgent, model)
	ctx, cancel := context.WithCancel(context.Background())
	return &Provider{
		model:      model,
		cfg:        cfg,
		baseCtx:    ctx,
		baseCancel: cancel,
		initDone:   make(chan struct{}),
		procExited: make(chan struct{}),
	}, nil
}

func (p *Provider) GetModel() string       { return p.model }
func (p *Provider) GetBackend() ai.Backend { return ai.BackendClaudeAgent }

// Execute drains its own ExecuteStream into a buffered ai.Response. When the
// request carries a structured-output schema, the validated JSON the SDK
// returns is unmarshalled into req.Prompt.Schema and surfaced as StructuredData
// (mirroring the genkit provider).
func (p *Provider) Execute(ctx context.Context, req ai.Request) (*ai.Response, error) {
	start := time.Now()
	events, err := p.ExecuteStream(ctx, req)
	if err != nil {
		return nil, err
	}

	var (
		text       strings.Builder
		usage      ai.Usage
		sessionID  string
		structured json.RawMessage
		subtype    string
		success    = true
		sawResult  bool
		lastErr    string
		outcome    *ai.TerminalOutcome
		outcomeErr error
	)
	for ev := range events {
		if outcomeErr == nil {
			parsed, parseErr := ai.TerminalOutcomeFromEvent(ev)
			if parseErr != nil {
				outcomeErr = parseErr
			} else if parsed != nil {
				outcome = parsed
			}
		}
		switch ev.Kind {
		case ai.EventText:
			text.WriteString(ev.Text)
		case ai.EventSystem:
			if ev.SessionID != "" {
				sessionID = ev.SessionID
			}
		case ai.EventResult:
			sawResult = true
			success = ev.Success
			if ev.Usage != nil {
				usage = *ev.Usage
			}
			if ev.SessionID != "" {
				sessionID = ev.SessionID
			}
			if len(ev.StructuredData) > 0 {
				structured = ev.StructuredData
			}
			if s, ok := ev.Input["subtype"].(string); ok {
				subtype = s
			}
		case ai.EventError:
			lastErr = ev.Error
		}
	}
	if outcomeErr != nil {
		return nil, fmt.Errorf("claude-agent: invalid terminal outcome: %w", outcomeErr)
	}

	if !sawResult && lastErr != "" {
		return nil, fmt.Errorf("%w: %s", ai.ErrCLIExecutionFailed, lastErr)
	}
	if sawResult && !success {
		return nil, p.resultError(req, subtype, lastErr)
	}

	resp := &ai.Response{
		Text:            text.String(),
		Model:           p.model,
		Backend:         ai.BackendClaudeAgent,
		Usage:           usage,
		Duration:        time.Since(start),
		TerminalOutcome: outcome,
	}
	if sessionID != "" {
		resp.Raw = map[string]any{"session_id": sessionID}
	}
	if outcome != nil {
		return resp, nil
	}
	if req.Prompt.Schema != nil {
		if err := ai.BindStructuredOutput(req.Prompt.Schema, structured); err != nil {
			return nil, err
		}
		resp.StructuredData = req.Prompt.Schema
		resp.Text = ""
	} else if len(req.Prompt.SchemaJSON) > 0 && len(structured) > 0 {
		// A pre-built JSON schema has no Go target to bind into; surface the raw
		// structured JSON and leave it on Text for tolerant decoders.
		resp.StructuredData = structured
		resp.Text = string(structured)
	}
	return resp, nil
}

// resultError builds the failure error for a turn that finished with is_error.
// A structured-output run that exhausted its validation retries reports a
// schema-validation failure so the caller can tell it apart from an agent crash.
func (p *Provider) resultError(req ai.Request, subtype, lastErr string) error {
	if req.Prompt.Schema != nil && subtype == "error_max_structured_output_retries" {
		return fmt.Errorf("%w: claude-agent could not produce output matching the schema after its retry limit", ai.ErrSchemaValidation)
	}
	msg := lastErr
	if msg == "" {
		msg = "claude-agent returned is_error=true"
	}
	return fmt.Errorf("%w: %s", ai.ErrCLIExecutionFailed, msg)
}

// ExecuteStream pushes one user turn to the SDK session and streams the mapped
// events back. Turns are serialized via turnMu so the single SDK session is
// never driven by two prompts at once.
func (p *Provider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	schema, err := requestSchemaJSON(req)
	if err != nil {
		return nil, err
	}
	// The SDK's outputFormat is fixed for the whole query() session, so the first
	// turn pins the schema and every later turn must match it (a text turn on a
	// structured session, or a differing schema, cannot be honoured).
	p.sessionSchemaOnce.Do(func() { p.sessionSchema = schema })

	if err := p.ensureStarted(req); err != nil {
		return nil, err
	}
	if !bytes.Equal(schema, p.sessionSchema) {
		return nil, fmt.Errorf("claude-agent: structured-output schema is fixed when the session starts and cannot change between turns")
	}

	events := make(chan ai.Event, 16)
	go p.runTurn(ctx, req, events)
	return events, nil
}

// requestSchemaJSON derives the JSON schema captain sends to the SDK from the
// request's structured-output target (a reflected Go struct or a verbatim
// Prompt.SchemaJSON), or nil for a text-mode request. A non-struct target fails
// loudly rather than silently dropping the schema.
func requestSchemaJSON(req ai.Request) (json.RawMessage, error) {
	schema, err := ai.SchemaJSONForBackend(ai.BackendClaudeAgent, req.Prompt)
	if err != nil {
		return nil, fmt.Errorf("claude-agent: cannot derive structured-output schema: %w", err)
	}
	return schema, nil
}

// Close shuts the SDK session down (best-effort shutdown RPC), stops the
// supervised process, and cancels the provider's base context.
func (p *Provider) Close() error {
	if p.rpc != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, _ = p.rpc.Call(ctx, methodShutdown, nil)
		cancel()
	}
	if p.sup != nil {
		p.sup.Stop()
	}
	if p.baseCancel != nil {
		p.baseCancel()
	}
	return nil
}

// ensureStarted provisions and supervises the process and waits for the
// initialize handshake exactly once. Subsequent calls return the cached result.
func (p *Provider) ensureStarted(req ai.Request) error {
	p.startOnce.Do(func() {
		if err := p.provisionAndSupervise(req); err != nil {
			p.setInitResult(err)
		}
		// On success, onChildStarted (or OnExit) calls setInitResult.
	})

	select {
	case <-p.initDone:
		return p.initErr
	case <-time.After(initTimeout):
		return fmt.Errorf("claude-agent: initialize handshake timed out after %s", initTimeout)
	}
}

func (p *Provider) provisionAndSupervise(req ai.Request) error {
	proc, err := newAgentProcess(p)
	if err != nil {
		return err
	}

	sup := proc.Supervise(exec.SuperviseOptions{
		RestartPolicy: exec.RestartNo,
		OnStarted: func(child *exec.Process) {
			go p.onChildStarted(child, req)
		},
		OnExit: func() {
			p.procExitedOnce.Do(func() { close(p.procExited) })
			// If the process exited before the handshake completed, surface a
			// loud error instead of letting ensureStarted wait out its timeout.
			p.setInitResult(fmt.Errorf("claude-agent: process exited before initialize completed"))
		},
	})
	p.sup = sup
	sup.Start()
	return nil
}

// onChildStarted binds a fresh jsonrpc.Client to the running child's stdio,
// starts the read loop, and performs the initialize handshake.
func (p *Provider) onChildStarted(child *exec.Process, req ai.Request) {
	stdin := child.Stdin()
	stdout := child.StdoutReader()
	if stdin == nil || stdout == nil {
		p.setInitResult(fmt.Errorf("claude-agent: child stdio pipes unavailable"))
		return
	}

	rpc := jsonrpc.New(stdin, stdout, false, jsonrpc.Handlers{
		OnNotification: p.onNotification,
		OnRequest:      p.onRequest,
	})
	p.rpc = rpc
	go func() { _ = rpc.Run(p.baseCtx) }()

	ctx, cancel := context.WithTimeout(p.baseCtx, initCallTimeout)
	defer cancel()
	if _, err := rpc.Call(ctx, methodInitialize, p.initializeParams(req)); err != nil {
		p.setInitResult(fmt.Errorf("claude-agent: initialize failed: %w", err))
		return
	}
	p.setInitResult(nil)
}

// initializeParams maps the first request + provider config onto the SDK
// Options the agent.ts initialize handler understands.
func (p *Provider) initializeParams(req ai.Request) initializeParams {
	// brokered means the caller wants to vet each tool over the can_use_tool
	// round-trip, so the SDK must consult canUseTool instead of auto-approving:
	// bypassPermissions / allowDangerouslySkipPermissions would skip it entirely.
	// The broker callback is a runtime concern, carried on the provider's Config.
	brokered := p.cfg.CanUseTool != nil

	mode := string(req.Permissions.Mode)
	allowed := req.Permissions.Tools.Allow
	if req.Permissions.HasPreset(api.PresetEdit) {
		if mode == "" {
			mode = "acceptEdits"
		}
		if len(allowed) == 0 {
			allowed = safeEditAllowlist
		}
	}
	if mode == "" {
		if brokered {
			mode = "default"
		} else {
			mode = "bypassPermissions"
		}
	}

	approvalMode := "auto"
	if brokered {
		approvalMode = "ask"
	}

	resume := req.SessionID
	if resume == "" {
		resume = p.cfg.SessionID
	}

	// Prefer the per-request budget (like claude-cli, which reads req.Budget.Cost)
	// and fall back to the config default, so both Claude backends resolve the
	// ceiling from the same source (finding A4).
	maxBudget := req.Budget.Cost
	if maxBudget == 0 {
		maxBudget = p.cfg.Budget.Cost
	}

	return initializeParams{
		Cwd:                req.Cwd(),
		Model:              aliasModel(p.model),
		SystemPrompt:       req.Prompt.System,
		AppendSystemPrompt: req.Prompt.AppendSystem,
		AllowedTools:       allowed,
		MaxTurns:           req.Budget.MaxTurns,
		MaxBudgetUsd:       maxBudget,
		PermissionMode:     mode,
		Resume:             resume,
		ApprovalMode:       approvalMode,
		OutputSchema:       p.sessionSchema,
		MonitorURL:         monitorHooksURL(req),
	}
}

// monitorHooksURL resolves the captain serve URL the SDK's session-monitoring
// hooks deliver lifecycle events to; empty disables injection (bare runs,
// CAPTAIN_MONITOR_HOOKS=off).
func monitorHooksURL(req ai.Request) string {
	if !api.MonitorHooksEnabled(req) {
		return ""
	}
	return api.ServeBaseURL()
}

type initializeParams struct {
	Cwd                string          `json:"cwd,omitempty"`
	Model              string          `json:"model,omitempty"`
	SystemPrompt       string          `json:"systemPrompt,omitempty"`
	AppendSystemPrompt string          `json:"appendSystemPrompt,omitempty"`
	AllowedTools       []string        `json:"allowedTools,omitempty"`
	MaxTurns           int             `json:"maxTurns,omitempty"`
	MaxBudgetUsd       float64         `json:"maxBudgetUsd,omitempty"`
	PermissionMode     string          `json:"permissionMode,omitempty"`
	Resume             string          `json:"resume,omitempty"`
	ApprovalMode       string          `json:"approvalMode,omitempty"`
	OutputSchema       json.RawMessage `json:"outputSchema,omitempty"`
	MonitorURL         string          `json:"monitorUrl,omitempty"`
}

func (p *Provider) setInitResult(err error) {
	p.initMu.Lock()
	defer p.initMu.Unlock()
	select {
	case <-p.initDone:
		return // already resolved
	default:
		p.initErr = err
		close(p.initDone)
	}
}
