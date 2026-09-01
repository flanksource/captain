// Package cmux implements captain's interactive-TUI provider: it drives a real
// claude/codex CLI inside a cmux.app terminal surface (terminal automation, not
// JSON-RPC), tailing the Claude session JSONL for structured progress. It backs
// the (anthropic, cmux) and (openai, cmux) runtimes.
//
// One Provider serves one family (claude or codex, derived from the model's provider).
// Each ExecuteStream spawns a goroutine that ensures a cmux workspace + terminal
// surface, launches the agent, submits the host-assembled prompt, and streams the
// resulting session events back as ai.Events, always ending in exactly one
// EventResult. Tool-permission dialogs are brokered through cfg.CanUseTool.
//
// Prompt assembly stays with the host: this provider pastes req.Prompt.User as-is.
package cmux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/commons/logger"
)

// log is the package-scoped logger for the cmux provider. Its level follows
// -v/--log-level and can be tuned with -Plog.level.cmux=debug.
var log = logger.GetLogger("cmux")

// Provider drives an interactive claude/codex TUI inside a cmux surface.
type Provider struct {
	model    string
	agent    string
	provider *api.ModelProvider
	cfg      ai.Config
}

var _ ai.StreamingProvider = (*Provider)(nil)

// New builds a cmux provider for the claude or codex agent, derived from the
// model's provider. The model name (cfg.Model.Name) may be empty, a bare agent
// name ("claude"/"codex"), or a concrete model ("opus"/"gpt-…").
func New(cfg ai.Config) (*Provider, error) {
	provider := cfg.Model.Provider
	agent, err := agentForProvider(provider)
	if err != nil {
		return nil, err
	}
	// cfg.Model.Name is already the exact id the CLI expects: api.NewProvider
	// resolved it before this factory ran.
	return &Provider{
		model:    cfg.Model.Name,
		agent:    agent,
		provider: provider,
		cfg:      cfg,
	}, nil
}

// agentForProvider names the local binary cmux pilots for a family. Only the
// families whose cmux cell the registry declares are served.
func agentForProvider(p *api.ModelProvider) (string, error) {
	if p != nil {
		if _, serves := p.Caps(api.ModeCmux); serves {
			return p.AgentName, nil
		}
	}
	return "", fmt.Errorf("cmux provider: %q has no cmux mode (available: %s)", providerLabel(p), api.RuntimeList())
}

func providerLabel(p *api.ModelProvider) string {
	if p == nil {
		return ""
	}
	return p.Name
}

func (p *Provider) GetModel() string { return p.model }

// GetRuntime reports the (provider, cmux) pair this adapter serves.
func (p *Provider) GetRuntime() ai.Runtime { return ai.RuntimeOf(p.provider, ai.ModeCmux) }

// Execute drains its own ExecuteStream into a buffered ai.Response. cmux cannot
// constrain output natively, so a structured-output request is served by
// appending a schema instruction to the prompt (see ExecuteStream); the stream
// carries the validated JSON on its terminal EventResult.
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
		success    = true
		sawResult  bool
		lastErr    string
		structured json.RawMessage
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
			if len(ev.StructuredData) > 0 {
				structured = ev.StructuredData
			}
			if ev.Usage != nil {
				usage = *ev.Usage
			}
			if ev.SessionID != "" {
				sessionID = ev.SessionID
			}
		case ai.EventError:
			lastErr = ev.Error
		}
	}
	if outcomeErr != nil {
		return nil, fmt.Errorf("cmux: invalid terminal outcome: %w", outcomeErr)
	}

	if !sawResult && lastErr != "" {
		return nil, fmt.Errorf("%w: %s", ai.ErrCLIExecutionFailed, lastErr)
	}
	if sawResult && !success {
		msg := lastErr
		if msg == "" {
			msg = "cmux run returned a failure result"
		}
		return nil, fmt.Errorf("%w: %s", ai.ErrCLIExecutionFailed, msg)
	}

	resp := &ai.Response{
		Text:            text.String(),
		Model:           p.model,
		Runtime:         p.GetRuntime(),
		Usage:           usage,
		Duration:        time.Since(start),
		TerminalOutcome: outcome,
	}
	if sessionID != "" {
		resp.Raw = map[string]any{"session_id": sessionID}
	}
	if len(structured) > 0 && req.Prompt.Schema != nil {
		if err := ai.BindStructuredOutput(req.Prompt.Schema, structured); err != nil {
			return nil, err
		}
		resp.StructuredData = req.Prompt.Schema
		resp.Text = ""
	} else if len(structured) > 0 {
		resp.StructuredData = structured
	}
	return resp, nil
}

// ExecuteStream drives one cmux run in a goroutine and streams ai.Events on a
// buffered channel, closing it when done (always after exactly one EventResult).
func (p *Provider) ExecuteStream(ctx context.Context, req ai.Request) (<-chan ai.Event, error) {
	req, schema, err := ai.WithSchemaPrompt(req)
	if err != nil {
		return nil, err
	}
	if req.Prompt.User == "" {
		return nil, fmt.Errorf("cmux provider: prompt is required")
	}
	// AgentCommand's codex branch emits no tool flags — codex has no equivalent —
	// so a policy set here would be dropped rather than applied.
	if err := api.RequireToolPolicySupport(p.provider, api.ModeCmux, req.Permissions); err != nil {
		return nil, err
	}
	events := make(chan ai.Event, 32)
	go p.drive(ctx, req, schema, events)
	return events, nil
}

// drive runs the session and translates its outcome into the single terminal
// EventResult (preceded by an EventError on failure) before closing the channel.
func (p *Provider) drive(ctx context.Context, req ai.Request, schema json.RawMessage, events chan ai.Event) {
	defer close(events)

	var text strings.Builder
	var outcome *ai.TerminalOutcome
	var outcomeErr error
	r := &run{
		client:     NewClient(""),
		canUseTool: p.cfg.CanUseTool,
	}
	r.emit = func(ev ai.Event) {
		switch ev.Kind {
		case ai.EventText:
			text.WriteString(ev.Text)
		case ai.EventToolUse:
			if ev.Tool == "ExitPlanMode" {
				r.planExitSeen = true
			}
			if outcomeErr == nil {
				parsed, err := ai.TerminalOutcomeFromEvent(ev)
				if err != nil {
					outcomeErr = err
				} else if parsed != nil {
					outcome = parsed
				}
			}
		}
		emit(ctx, events, ev)
	}

	usage, cost, err := p.execute(ctx, req, r)
	if err == nil && outcomeErr != nil {
		err = fmt.Errorf("cmux: invalid terminal outcome: %w", outcomeErr)
	}
	var structured json.RawMessage
	if err == nil {
		structured, err = ai.ValidatedStructuredData(schema, text.String(), outcome)
	}
	if err != nil {
		emit(ctx, events, ai.Event{Kind: ai.EventError, Error: err.Error(), Model: p.model})
		emit(ctx, events, ai.Event{Kind: ai.EventResult, Success: false, Error: err.Error(), Model: p.model})
		return
	}
	emit(ctx, events, ai.Event{Kind: ai.EventResult, Success: true, Usage: usage, CostUSD: cost, Model: p.model, StructuredData: structured})
}

// cmuxExtraArgs decodes req.CLIArgs into the backend's typed extra arguments and
// projects the unified sandbox policy onto the provider-native flags/settings.
func cmuxExtraArgs(agent string, req ai.Request) (any, error) {
	switch agent {
	case "codex":
		opts := api.CodexCmuxOptions{}
		if err := decodeCLIArgs(req.CLIArgs, &opts); err != nil {
			return nil, err
		}
		if req.Sandbox == nil {
			return &opts, nil
		}
		if opts.Sandbox != "" || opts.AskForApproval != "" {
			return nil, fmt.Errorf("sandbox conflicts with cliArgs sandbox/askForApproval")
		}
		translation, err := api.TranslateCodexSandbox(api.RuntimeOf(api.OpenAI, api.ModeCmux), req.Sandbox, req.Permissions.Mode)
		if err != nil {
			return nil, err
		}
		opts.Sandbox = translation.Sandbox
		opts.AskForApproval = translation.Approval
		opts.Config = append(opts.Config, translation.ConfigArgs()...)
		return &opts, nil
	default:
		opts := api.ClaudeCmuxOptions{}
		if err := decodeCLIArgs(req.CLIArgs, &opts); err != nil {
			return nil, err
		}
		if req.Sandbox == nil {
			return &opts, nil
		}
		if opts.Settings != "" {
			return nil, fmt.Errorf("sandbox conflicts with cliArgs.settings")
		}
		sandbox, err := api.TranslateClaudeSandbox(api.RuntimeOf(api.Anthropic, api.ModeCmux), *req.Sandbox)
		if err != nil {
			return nil, err
		}
		settings, err := json.Marshal(map[string]any{"sandbox": sandbox})
		if err != nil {
			return nil, fmt.Errorf("encode Claude cmux sandbox settings: %w", err)
		}
		opts.Settings = string(settings)
		return &opts, nil
	}
}

// decodeCLIArgs round-trips the free-form CLIArgs map through JSON into the typed
// option struct, failing loud on a type mismatch rather than silently dropping it.
func decodeCLIArgs(args map[string]any, out any) error {
	if len(args) == 0 {
		return nil
	}
	data, err := json.Marshal(args)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// execute mirrors gavel's CmuxExecutor.ExecuteGroup: it ensures a workspace +
// surface, launches the agent, submits the prompt, and (for claude) tails the
// session log under a stall watchdog. It returns the run's usage/cost on success,
// or an error describing the failure. Streaming events are emitted via r.emit.
func (p *Provider) execute(ctx context.Context, req ai.Request, r *run) (*ai.Usage, float64, error) {
	start := time.Now()
	agent := p.agent
	approval := req.Permissions.Mode
	r.planMode = approval == api.PermissionPlan
	model := modelFlag(agent, p.model)
	workDir := groupWorkDir(req.Cwd())

	// Resolve the Claude session id. Resume reuses req.SessionID (launch with
	// --resume, tail from the end). A fresh run pre-generates one (or takes
	// cfg.SessionID) and launches with --session-id so the session log can be
	// tailed. codex manages its own sessions and keeps the screen-idle path.
	sessionID := ""
	resume := false
	if agent == "claude" {
		if req.SessionID != "" {
			sessionID = req.SessionID
			resume = true
		} else if p.cfg.SessionID != "" {
			sessionID = p.cfg.SessionID
		} else {
			sessionID = uuid.NewString()
		}
	}
	if sessionID != "" {
		r.emit(ai.Event{Kind: ai.EventSystem, SessionID: sessionID})
	}

	extra, err := cmuxExtraArgs(agent, req)
	if err != nil {
		return nil, 0, fmt.Errorf("cmux: invalid cliArgs: %w", err)
	}
	agentCommand := withEnv(AgentCommand(AgentCommandOpts{
		Agent:           agent,
		Model:           model,
		SessionID:       sessionID,
		Resume:          resume,
		Plan:            approval == api.PermissionPlan,
		PermissionMode:  approval,
		AllowedTools:    req.Permissions.Tools.AllowList(),
		DisallowedTools: req.Permissions.Tools.DenyList(),
		Effort:          req.Effort,
		Memory:          req.Memory,
		Extra:           extra,
	}), req.EnvMap())

	timeout := r.timeout()
	// ctxLog routes internal step-by-step narration to the run's clicky task
	// (its own buffered trace log) when one is attached to ctx, falling back to
	// the plain "cmux" logger for callers with no task (e.g. bare-CLI runs).
	ctxLog := ai.LoggerFromContext(ctx, log)
	log.Infof("cmux: dispatching with %s (model=%s) in %s", agent, model, workDir)
	log.Debugf("cmux command: cmux ping")
	if err := r.client.Available(ctx); err != nil {
		return nil, 0, err
	}

	name := AgentWorkspaceName(workDir, agent)
	ctxLog.Tracef("cmux: ensuring workspace %q for %s", name, agent)
	workspace, reused, err := r.client.EnsureWorkspace(ctx, EnsureWorkspaceOpts{
		Cwd:         workDir,
		Name:        name,
		Description: fmt.Sprintf("gavel todos %s workspace for %s", agent, workDir),
		Focus:       true,
	})
	if err != nil {
		return nil, 0, err
	}
	if reused {
		ctxLog.Tracef("cmux: reusing workspace %s", workspace.String())
	} else {
		ctxLog.Tracef("cmux: created workspace %s", workspace.String())
	}

	ctxLog.Tracef("cmux: creating %s terminal surface in workspace %s", agent, workspace.String())
	ref, err := r.client.NewSurface(ctx, NewSurfaceOpts{
		WorkspaceRef: workspace.String(),
		Cwd:          workDir,
		SurfaceType:  "terminal",
		Focus:        true,
	})
	if err != nil {
		return nil, 0, err
	}

	ctxLog.Tracef("cmux: waiting for terminal surface to stabilize before launching %s", agent)
	beforeAgentScreen, err := r.waitForScreenIdle(ctx, ref, "before agent launch", timeout, "", false)
	if err != nil {
		return nil, 0, err
	}

	if err := r.sendSurfaceText(ctx, ref, "agent command", agentCommand); err != nil {
		return nil, 0, err
	}

	// Wait until the agent is ready for the prompt. claude gates on a positive
	// REPL-readiness signal (its input prompt appearing); codex keeps screen-idle.
	ctxLog.Tracef("cmux: waiting for %s to be ready for the prompt", agent)
	var beforePromptScreen string
	if agent == "claude" {
		if _, err := r.waitForREPLReady(ctx, ref, timeout, beforeAgentScreen); err != nil {
			return nil, 0, err
		}
	} else {
		beforePromptScreen, err = r.waitForScreenIdle(ctx, ref, "after agent launch", timeout, beforeAgentScreen, true)
		if err != nil {
			return nil, 0, err
		}
	}

	instruction, err := r.buildInstruction(workDir, sessionID, req.Prompt.User)
	if err != nil {
		return nil, 0, err
	}

	if sessionID != "" {
		logPath, err := SessionLogPath(workDir, sessionID)
		if err != nil {
			return nil, 0, err
		}
		// The initial prompt's Enter occasionally gets dropped by cmux, leaving the
		// prompt typed but unsent. submitAndConfirm re-presses Enter until the
		// session demonstrably started (its log appeared or the surface advanced).
		if err := r.submitAndConfirm(ctx, ref, "initial prompt", instruction, submitConfirm{logPath: logPath}); err != nil {
			return nil, 0, err
		}

		// Register the run as a live in-progress session so the dashboard reads
		// token/cost totals from the tailer. Finish freezes the elapsed clock.
		acc := GlobalSessionStats().Begin(sessionID, agent, model, string(req.Effort), start)
		_, completed, serr := r.awaitWithStallWatchdog(ctx, ref, sessionID, workDir, timeout, resume, acc)
		acc.Finish()
		snap := acc.snapshot()
		pausedForQuestion := snap.State == sessionStateAsk
		if err := r.dismissCompletedPlan(ctx, ref, sessionID); err != nil {
			return nil, 0, err
		}
		if pausedForQuestion {
			// Claude's terminal UI remains inside the interactive question picker even
			// though the structured tool event contains everything the host needs to
			// render the form. Dismiss that surface once; the host resumes the same
			// session later with SendFeedback and a structured answers payload.
			if r.planMode {
				if err := r.dismissPlanSurface(ctx, ref, sessionID); err != nil {
					return nil, 0, err
				}
			} else if err := r.client.SendKeySurface(ctx, ref.String(), ref.SurfaceID, "Escape"); err != nil {
				return nil, 0, fmt.Errorf("dismiss claude question for session %s: %w", sessionID, err)
			}
		}

		switch {
		case errors.Is(serr, errSessionLogNotFound):
			// A pre-generated claude session must produce its log; if it never
			// appears we fail loudly rather than inferring completion from the screen.
			return nil, 0, fmt.Errorf("claude session %s log %s did not appear within %s", sessionID, logPath, r.sessionLogAppearTimeout(timeout))
		case serr != nil:
			return nil, 0, serr
		case !completed:
			return nil, 0, fmt.Errorf("claude session %s did not complete within %s", sessionID, timeout)
		default:
			if pausedForQuestion {
				log.Infof("cmux: claude session %s paused for user input", sessionID)
			} else {
				log.Infof("cmux: claude session %s completed", sessionID)
			}
			r.lastSurface = ref
			r.lastSessionID = sessionID
			r.lastWorkDir = workDir
			usage := usageFromStats(snap)
			return &usage, snap.CostUSD, nil
		}
	}

	// codex: no session log to tail, so completion is the screen settling.
	//
	// codex has no session log, so submission confirmation is purely the surface
	// advancing past the just-submitted screen. submitAndConfirm re-presses Enter
	// until it does, closing the silent no-submit where waitForScreenIdle would
	// otherwise settle on the typed-but-unsent prompt and report success.
	if err := r.submitAndConfirm(ctx, ref, "initial prompt", instruction, submitConfirm{}); err != nil {
		return nil, 0, err
	}
	ctxLog.Tracef("cmux: waiting for %s screen to change and stabilize after prompt dispatch", agent)
	if _, err := r.waitForScreenIdle(ctx, ref, "after prompt dispatch", timeout, beforePromptScreen, true); err != nil {
		return nil, 0, err
	}
	return nil, 0, nil
}

// usageFromStats projects the session accumulator's token totals onto an
// ai.Usage so the terminal EventResult carries the run's token usage.
func usageFromStats(s SessionStats) ai.Usage {
	return ai.Usage{
		InputTokens:      s.InputTokens,
		OutputTokens:     s.OutputTokens,
		CacheReadTokens:  s.CacheReadTokens,
		CacheWriteTokens: s.CacheCreationTokens,
	}
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
