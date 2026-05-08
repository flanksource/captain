package cli

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/ai/provider"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/commons/logger"
)

// loadSavedAI returns the saved AI defaults from ~/.captain.yaml. Errors are
// surfaced as zero-valued defaults rather than failing the command — a missing
// or unreadable config should never block `captain ai prompt`.
func loadSavedAI() captainconfig.AIDefaults {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		logger.Debugf("captainconfig load: %v (continuing with zero defaults)", err)
		return captainconfig.AIDefaults{}
	}
	return cfg.AI
}

type AIProviderOptions struct {
	Model   string `flag:"model" help:"Model name, e.g. claude-sonnet-4, gemini-2.0-flash (defaults to the value saved by 'captain configure')" short:"m"`
	Backend string `flag:"backend" help:"Force backend: anthropic, gemini, codex-cli, claude-cli, gemini-cli (default: inferred from model or saved by 'captain configure')" short:"b"`
	APIKey  string `flag:"api-key" help:"API key (env: ANTHROPIC_API_KEY, GEMINI_API_KEY, GOOGLE_API_KEY)"`
	NoCache bool   `flag:"no-cache" help:"Disable response caching"`
	Budget  string `flag:"budget" help:"Max spend in USD, 0=unlimited" default:"0"`
	Debug   bool   `flag:"debug" help:"Enable debug logging for HTTP requests"`
}

func (o AIProviderOptions) ToConfig() ai.Config {
	saved := loadSavedAI()
	budget, _ := strconv.ParseFloat(o.Budget, 64)

	model := o.Model
	if model == "" {
		model = saved.Model
	}
	backend := o.Backend
	if backend == "" {
		backend = saved.Backend
	}
	if budget == 0 {
		budget = saved.BudgetUSD
	}
	noCache := o.NoCache || saved.NoCache

	cfg := ai.Config{
		Model:     model,
		Backend:   ai.Backend(backend),
		APIKey:    o.APIKey,
		NoCache:   noCache,
		BudgetUSD: budget,
		Debug:     o.Debug,
	}
	if o.Debug || logger.IsDebugEnabled() {
		cfg.HTTPClient = provider.NewLoggingHTTPClient()
	}
	return cfg
}

type AIPromptOptions struct {
	AIProviderOptions
	Prompt       string `flag:"prompt" help:"Prompt text" short:"p" required:"true" stdin:"true"`
	System       string `flag:"system" help:"System prompt" short:"s"`
	AppendSystem string `flag:"append-system" help:"Append text to the default system prompt"`
	MaxTokens    int    `flag:"max-tokens" help:"Maximum output tokens" default:"4096"`
	Temperature  string `flag:"temperature" help:"Sampling temperature" default:"0"`
	Timeout      string `flag:"timeout" help:"Request timeout" default:"120s"`

	NoStream bool `flag:"no-stream" help:"Disable streaming; print only the final text to stdout"`

	Edit            bool     `flag:"edit" help:"Safe defaults: acceptEdits + Read/Edit/Write/Glob/Grep allowlist"`
	AllowedTools    []string `flag:"allowed-tools" help:"Override --edit's built-in allowlist (claude only)"`
	DisallowedTools []string `flag:"disallowed-tools" help:"Tools to deny (claude only)"`
	PermissionMode  string   `flag:"permission-mode" help:"acceptEdits|auto|bypassPermissions|default|plan"`

	MCP       bool     `flag:"mcp" help:"Set --mcp=false to disable all MCP servers" default:"true"`
	Hooks     bool     `flag:"hooks" help:"Set --hooks=false to skip hooks" default:"true"`
	Skills    bool     `flag:"skills" help:"Set --skills=false to disable slash commands" default:"true"`
	SkillDirs []string `flag:"skill-dir" help:"Additional skill/plugin directory (repeatable)"`
	User      bool     `flag:"user" help:"Load user-level settings" default:"true"`
	Project   bool     `flag:"project" help:"Load project-level settings" default:"true"`
	Memory    bool     `flag:"memory" help:"Load auto-memory and CLAUDE.md" default:"true"`
	Bare      bool     `flag:"bare" help:"Skip hooks, skills, memory, and ambient settings"`
}

type AIPromptResult struct {
	Text     string  `json:"text" pretty:"label=Response"`
	Model    string  `json:"model" pretty:"label=Model"`
	Backend  string  `json:"backend" pretty:"label=Backend"`
	Input    int     `json:"inputTokens" pretty:"label=Input Tokens"`
	Output   int     `json:"outputTokens" pretty:"label=Output Tokens"`
	CostUSD  float64 `json:"costUSD,omitempty" pretty:"label=Cost USD"`
	Duration string  `json:"duration" pretty:"label=Duration"`
}

// ToRequest translates the user-facing AIPromptOptions into the typed
// ai.Request. Truthy flags like --mcp/--hooks/--memory invert into the
// No*-style fields the providers consume. When --bare is set, it implicitly
// strips memory/hooks/skills/user/project regardless of those flags' values
// (claude --bare composes them) so we let the provider decide the final argv.
//
// Saved defaults from ~/.captain.yaml overlay onto unset fields. For the
// boolean toggles, "saved off" wins over "flag default on" because clicky does
// not yet expose a Changed() bit.
//
// WORKAROUND(no-flag-changed-bit): The boolean toggles default to true, so we
// cannot tell whether --mcp=true was passed explicitly or inherited from the
// default. As a result, when the saved config has NoMCP=true the user cannot
// force MCP back on from the command line by passing --mcp=true alone — they
// must edit ~/.captain.yaml or rerun `captain configure`.
// Correct fix: thread clicky's per-flag Changed() bit (or a tri-state flag
// type) through AIPromptOptions so we can distinguish "explicitly true" from
// "default true". Ref: discussed with user 2026-05-07.
func (o AIPromptOptions) ToRequest() ai.Request {
	saved := loadSavedAI()
	temperature, _ := strconv.ParseFloat(o.Temperature, 64)

	maxTokens := o.MaxTokens
	if maxTokens == 4096 && saved.MaxTokens != 0 {
		maxTokens = saved.MaxTokens
	}

	effort := saved.ReasoningEffort

	return ai.Request{
		SystemPrompt:       o.System,
		AppendSystemPrompt: o.AppendSystem,
		Prompt:             o.Prompt,
		MaxTokens:          maxTokens,
		Temperature:        temperature,
		ReasoningEffort:    effort,
		PermissionMode:     o.PermissionMode,
		Edit:               o.Edit,
		AllowedTools:       o.AllowedTools,
		DisallowedTools:    o.DisallowedTools,
		NoMCP:              !o.MCP || saved.NoMCP,
		NoHooks:            !o.Hooks || saved.NoHooks,
		NoSkills:           !o.Skills || saved.NoSkills,
		SkillDirs:          o.SkillDirs,
		NoUser:             !o.User || saved.NoUser,
		NoProject:          !o.Project || saved.NoProject,
		NoMemory:           !o.Memory || saved.NoMemory,
		Bare:               o.Bare,
	}
}

func RunAIPrompt(opts AIPromptOptions) (any, error) {
	if opts.Prompt == "" {
		return nil, fmt.Errorf("prompt text required (use --prompt or pipe via stdin)")
	}

	cfg := opts.ToConfig()
	if cfg.Model == "" {
		return nil, fmt.Errorf("no model: pass --model or run 'captain configure' to set a default")
	}

	req := opts.ToRequest()

	p, err := ai.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	if p, err = middleware.Wrap(p, middleware.WithLogging()); err != nil {
		return nil, err
	}

	timeout, _ := time.ParseDuration(opts.Timeout)
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if streamer, ok := p.(ai.StreamingProvider); ok && !opts.NoStream {
		return runStreaming(ctx, streamer, req)
	}
	return runBuffered(ctx, p, req)
}

func runBuffered(ctx context.Context, p ai.Provider, req ai.Request) (any, error) {
	start := time.Now()
	resp, err := p.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	return AIPromptResult{
		Text:     resp.Text,
		Model:    resp.Model,
		Backend:  string(resp.Backend),
		Input:    resp.Usage.InputTokens,
		Output:   resp.Usage.OutputTokens,
		Duration: time.Since(start).Round(time.Millisecond).String(),
	}, nil
}

// runStreaming drives the streaming provider through ai.RunUntil with a single
// iteration, rendering live tool/text events to stderr while accumulating the
// final text/usage/cost into AIPromptResult on stdout.
func runStreaming(ctx context.Context, sp ai.StreamingProvider, req ai.Request) (any, error) {
	start := time.Now()
	var (
		text    string
		usage   ai.Usage
		cost    float64
		backend = string(sp.GetBackend())
		model   = sp.GetModel()
	)
	renderer := newLineRenderer(os.Stderr, 8)
	loop, err := ai.RunUntil(ctx, ai.LoopOptions{
		Provider:      sp,
		MaxIterations: 1,
		BuildRequest: func(iter int, prev *ai.LoopIteration) (ai.Request, bool) {
			if iter > 0 {
				return ai.Request{}, false
			}
			return req, true
		},
		OnEvent: func(_ int, ev ai.Event) {
			if ev.Model != "" {
				model = ev.Model
			}
			renderEvent(os.Stderr, renderer, ev)
			if ev.Kind == ai.EventText {
				text += ev.Text
			}
			if ev.Kind == ai.EventResult {
				if ev.Usage != nil {
					usage = *ev.Usage
				}
				cost = ev.CostUSD
			}
		},
	})
	if err != nil {
		return nil, err
	}
	if loop.StopReason == "error" {
		return nil, fmt.Errorf("streaming loop stopped: %s", loop.StopReason)
	}
	return AIPromptResult{
		Text:     text,
		Model:    model,
		Backend:  backend,
		Input:    usage.InputTokens,
		Output:   usage.OutputTokens,
		CostUSD:  cost,
		Duration: time.Since(start).Round(time.Millisecond).String(),
	}, nil
}

// renderEvent writes a human-readable representation of an ai.Event to w.
// When the event carries a claude.HistoryEntry in Raw, route through the
// shared lineRenderer so live `captain ai prompt` output matches
// `captain history` for the same tools (including session-start banners).
func renderEvent(w *os.File, renderer *lineRenderer, ev ai.Event) {
	if entry, ok := ev.Raw.(claude.HistoryEntry); ok {
		if renderClaudeEntry(renderer, ev, entry) {
			return
		}
	}

	switch ev.Kind {
	case ai.EventText:
		fmt.Fprintf(w, "%s", ev.Text)
	case ai.EventThinking:
		if logger.IsDebugEnabled() {
			fmt.Fprintf(w, "[thinking] %s\n", truncForStderr(ev.Text, 200))
		}
	case ai.EventToolUse:
		fmt.Fprintf(w, "\n[tool] %s %s\n", ev.Tool, summariseInput(ev.Input))
	case ai.EventResult:
		renderResultEvent(renderer, ev)
	case ai.EventError:
		fmt.Fprintf(w, "\n[error] %s\n", ev.Error)
		logger.Errorf("%s", ev.Error)
	case ai.EventSystem:
		if ev.SessionID != "" {
			fmt.Fprintf(w, "[session] %s\n", ev.SessionID)
		}
	}
}

// renderResultEvent synthesizes a Result tools.Tool from the ai.Event so
// streaming output renders end-of-session result lines with the same
// "🏁 result turns=N $X 1.2s" formatting as `captain history`.
func renderResultEvent(renderer *lineRenderer, ev ai.Event) {
	input := map[string]any{}
	for k, v := range ev.Input {
		input[k] = v
	}
	if ev.CostUSD > 0 {
		if _, ok := input["total_cost_usd"]; !ok {
			input["total_cost_usd"] = ev.CostUSD
		}
	}
	if !ev.Success {
		input["is_error"] = true
		if _, ok := input["result"]; !ok && ev.Error != "" {
			input["result"] = ev.Error
		}
	}
	base := tools.BaseTool{
		RawTool:   "Result",
		Input:     input,
		Timestamp: nil,
	}
	if ev.Usage != nil && (ev.Usage.InputTokens > 0 || ev.Usage.OutputTokens > 0) {
		base.Models = tools.Models{{
			Model:        ev.Model,
			InputTokens:  ev.Usage.InputTokens,
			OutputTokens: ev.Usage.OutputTokens,
		}}
	}
	renderer.Render(tools.NewTool(base), true)
}

// renderClaudeEntry feeds a claude HistoryEntry through the shared lineRenderer
// so live streaming output uses the same row format and session-start banners
// as `captain history`. Both real tool uses and synthetic Result/SessionInit
// entries flow through the same rendering path. Returns false when there is
// nothing renderable so the caller can fall back to generic event handling.
func renderClaudeEntry(renderer *lineRenderer, ev ai.Event, entry claude.HistoryEntry) bool {
	switch ev.Kind {
	case ai.EventToolUse, ai.EventResult, ai.EventSystem:
	default:
		return false
	}
	tl := claude.ExtractToolsWithTokens([]claude.HistoryEntry{entry})
	if len(tl) == 0 {
		return false
	}
	for _, t := range tl {
		renderer.Render(t, true)
	}
	return true
}

func summariseInput(input map[string]any) string {
	if len(input) == 0 {
		return ""
	}
	for _, key := range []string{"file_path", "path", "command", "pattern", "url"} {
		if v, ok := input[key].(string); ok && v != "" {
			return truncForStderr(v, 80)
		}
	}
	return ""
}

func truncForStderr(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

type AITestOptions struct {
	AIProviderOptions
	Timeout string `flag:"timeout" help:"Request timeout" default:"60s"`
}

type AITestResult struct {
	Model   string `json:"model" pretty:"label=Model"`
	Backend string `json:"backend" pretty:"label=Backend"`
	Status  string `json:"status" pretty:"label=Status"`
	Latency string `json:"latency" pretty:"label=Latency"`
}

func RunAITest(opts AITestOptions) (any, error) {
	cfg := opts.ToConfig()
	if cfg.Model == "" {
		return nil, fmt.Errorf("no model: pass --model or run 'captain configure' to set a default")
	}
	p, err := ai.NewProvider(cfg)
	if err != nil {
		return nil, err
	}
	if p, err = middleware.Wrap(p, middleware.WithLogging()); err != nil {
		return nil, err
	}
	timeout, _ := time.ParseDuration(opts.Timeout)
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	start := time.Now()
	_, err = p.Execute(ctx, ai.Request{
		Prompt:    "Respond with exactly: ok",
		MaxTokens: 10,
	})

	result := AITestResult{
		Model:   p.GetModel(),
		Backend: string(p.GetBackend()),
		Latency: time.Since(start).Round(time.Millisecond).String(),
	}

	if err != nil {
		result.Status = fmt.Sprintf("FAIL: %v", err)
	} else {
		result.Status = "OK"
	}
	return result, nil
}
