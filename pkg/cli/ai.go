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
	"github.com/flanksource/commons/logger"
)

type AIProviderOptions struct {
	Model   string `flag:"model" help:"Model name, e.g. claude-sonnet-4, gemini-2.0-flash" short:"m" required:"true"`
	Backend string `flag:"backend" help:"Force backend: anthropic, gemini, codex-cli, claude-cli, gemini-cli (default: inferred from model)" short:"b"`
	APIKey  string `flag:"api-key" help:"API key (env: ANTHROPIC_API_KEY, GEMINI_API_KEY, GOOGLE_API_KEY)"`
	NoCache bool   `flag:"no-cache" help:"Disable response caching"`
	Budget  string `flag:"budget" help:"Max spend in USD, 0=unlimited" default:"0"`
	Debug   bool   `flag:"debug" help:"Enable debug logging for HTTP requests"`
}

func (o AIProviderOptions) ToConfig() ai.Config {
	budget, _ := strconv.ParseFloat(o.Budget, 64)
	cfg := ai.Config{
		Model:     o.Model,
		Backend:   ai.Backend(o.Backend),
		APIKey:    o.APIKey,
		NoCache:   o.NoCache,
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
func (o AIPromptOptions) ToRequest() ai.Request {
	temperature, _ := strconv.ParseFloat(o.Temperature, 64)
	return ai.Request{
		SystemPrompt:       o.System,
		AppendSystemPrompt: o.AppendSystem,
		Prompt:             o.Prompt,
		MaxTokens:          o.MaxTokens,
		Temperature:        temperature,
		PermissionMode:     o.PermissionMode,
		Edit:               o.Edit,
		AllowedTools:       o.AllowedTools,
		DisallowedTools:    o.DisallowedTools,
		NoMCP:              !o.MCP,
		NoHooks:            !o.Hooks,
		NoSkills:           !o.Skills,
		SkillDirs:          o.SkillDirs,
		NoUser:             !o.User,
		NoProject:          !o.Project,
		NoMemory:           !o.Memory,
		Bare:               o.Bare,
	}
}

func RunAIPrompt(opts AIPromptOptions) (any, error) {
	if opts.Prompt == "" {
		return nil, fmt.Errorf("prompt text required (use --prompt or pipe via stdin)")
	}

	req := opts.ToRequest()

	p, err := ai.NewProvider(opts.ToConfig())
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
			renderEvent(os.Stderr, ev)
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

// renderEvent writes a one-line, human-readable representation of an
// ai.Event to w. Used by --stream to emit tool/token/step progress on stderr
// while the final response collects on stdout.
func renderEvent(w *os.File, ev ai.Event) {
	switch ev.Kind {
	case ai.EventText:
		fmt.Fprintf(w, "%s", ev.Text)
	case ai.EventThinking:
		fmt.Fprintf(w, "[thinking] %s\n", truncForStderr(ev.Text, 200))
	case ai.EventToolUse:
		fmt.Fprintf(w, "\n[tool] %s %s\n", ev.Tool, summariseInput(ev.Input))
	case ai.EventResult:
		if ev.Success {
			fmt.Fprintf(w, "\n[result] tokens=%d/%d cost=$%.4f\n",
				safeUsageInput(ev.Usage), safeUsageOutput(ev.Usage), ev.CostUSD)
		} else {
			fmt.Fprintf(w, "\n[result] FAIL: %s\n", ev.Error)
		}
	case ai.EventError:
		fmt.Fprintf(w, "\n[error] %s\n", ev.Error)
	case ai.EventSystem:
		if ev.SessionID != "" {
			fmt.Fprintf(w, "[session] %s\n", ev.SessionID)
		}
	}
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

func safeUsageInput(u *ai.Usage) int {
	if u == nil {
		return 0
	}
	return u.InputTokens
}

func safeUsageOutput(u *ai.Usage) int {
	if u == nil {
		return 0
	}
	return u.OutputTokens
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
	p, err := ai.NewProvider(opts.ToConfig())
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
