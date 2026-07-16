package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/claude"
	"github.com/flanksource/captain/pkg/claude/tools"
	"github.com/flanksource/captain/pkg/collections"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/shell"
)

// loadSavedAI returns the saved AI defaults from ~/.captain.yaml. Errors are
// surfaced as zero-valued defaults rather than failing the command — a missing
// or unreadable config should never block `captain ai prompt`.
func loadSavedAI() captainconfig.AIDefaults {
	return loadSavedConfig().AI
}

func loadSavedConfig() captainconfig.Config {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		log.Warnf("captainconfig load: %v (continuing with zero defaults)", err)
		return captainconfig.Config{}
	}
	return cfg
}

type AIProviderOptions struct {
	Model    string   `flag:"model" help:"Model name(s), e.g. claude-sonnet-5 or a comma-separated primary,fallback list like claude-sonnet-5,gpt-4o (defaults to the value saved by 'captain configure')" short:"m"`
	Fallback []string `flag:"fallback" help:"Model to try if the primary is unavailable (repeatable; comma-separated allowed)"`
	Backend  string   `flag:"backend" help:"Force backend: anthropic|gemini|openai|deepseek|claude-cli|claude-agent|claude-cmux|codex-cli|codex-agent|codex-cmux|gemini-cli (default: inferred from model or saved by 'captain configure')" short:"b"`
	APIKey   string   `flag:"api-key" help:"API key (env: ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, GOOGLE_API_KEY, DEEPSEEK_API_KEY)"`
	NoCache  bool     `flag:"no-cache" help:"Disable response caching"`
	Budget   string   `flag:"budget" help:"Max spend in USD, 0=unlimited" default:"0"`
}

// parseFloatFlag parses a numeric string flag, returning a descriptive error
// instead of silently coercing malformed input to zero.
func parseFloatFlag(name, val string) (float64, error) {
	if val == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid --%s %q: %w", name, val, err)
	}
	return f, nil
}

func (o AIProviderOptions) ToConfig() (ai.Config, error) {
	savedCfg := loadSavedConfig()
	saved := savedCfg.AI
	budget, err := parseFloatFlag("budget", o.Budget)
	if err != nil {
		return ai.Config{}, err
	}

	model := o.Model
	if model == "" {
		model = saved.Model
	}
	backend := o.Backend
	if backend == "" {
		backend = saved.Backend
	}
	if o.Backend == "" && ai.ContainsRuntimeSelector(model) {
		backend = ""
	}
	if backend != "" && !ai.Backend(backend).Valid() {
		return ai.Config{}, fmt.Errorf("invalid --backend %q (valid: %s)", backend, ai.BackendList())
	}
	if budget == 0 {
		budget = saved.BudgetUSD
	}

	m := api.Model{Name: model, Backend: ai.Backend(backend), Fallbacks: fallbackModelsFromFlags(o.Fallback)}
	m, err = ai.ResolveModelSelectors(m)
	if err != nil {
		return ai.Config{}, err
	}
	return ai.Config{
		Model:        m,
		Budget:       api.Budget{Cost: budget},
		APIKey:       o.APIKey,
		NoCache:      o.NoCache || saved.NoCache,
		SchemaRepair: schemaRepairConfig(savedCfg.Prompts.SchemaRepair),
	}, nil
}

func schemaRepairConfig(saved captainconfig.SchemaRepairDefaults) api.SchemaRepairConfig {
	return api.SchemaRepairConfig{
		Model:  api.Model{Name: saved.Model, Backend: api.Backend(saved.Backend)},
		Prompt: strings.TrimSpace(saved.Prompt),
	}
}

func isZeroSchemaRepair(c api.SchemaRepairConfig) bool {
	return strings.TrimSpace(c.Prompt) == "" &&
		c.Model.Name == "" &&
		c.Model.ID == "" &&
		c.Model.Backend == "" &&
		c.Model.Temperature == nil &&
		c.Model.Effort == "" &&
		!c.Model.NoCache &&
		len(c.Model.Fallbacks) == 0
}

// AIRuntimeOptions binds the per-invocation knobs every AI command shares —
// model selection (via embedded AIProviderOptions), generation parameters
// (max tokens, temperature, timeout, reasoning), permission/sandbox toggles
// (edit, allowed/disallowed tools, permission mode), and ambient-context
// toggles (mcp/hooks/skills/user/project/memory/bare). It deliberately
// omits the user-prompt fields so non-prompt commands (e.g. gavel's lint
// --ai-fix loop) can embed it without inheriting a required --prompt flag.
//
// AIPromptOptions embeds this struct and adds Prompt/System/AppendSystem/
// NoStream on top.
type AIRuntimeOptions struct {
	AIProviderOptions

	MaxTokens   int    `flag:"max-tokens" help:"Maximum output tokens (0 = saved default or 4096)"`
	Temperature string `flag:"temperature" help:"Sampling temperature (0.0-2.0)" default:"0"`
	Effort      string `flag:"effort" help:"Reasoning effort: low|medium|high|xhigh|max|ultra (model-dependent)"`
	MaxTurns    int    `flag:"max-turns" help:"Max agent turns 0-100, 0 = provider default (claude-agent)"`
	Resume      string `flag:"resume" help:"Resume an existing session by id (claude-agent, codex)"`

	Edit            bool     `flag:"edit" help:"Safe defaults: acceptEdits + Read/Edit/Write/Glob/Grep allowlist"`
	AllowedTools    []string `flag:"allowed-tools" help:"Override --edit's built-in allowlist (claude only)"`
	DisallowedTools []string `flag:"disallowed-tools" help:"Tools to deny (claude only)"`
	PermissionMode  string   `flag:"permission-mode" help:"acceptEdits|auto|bypassPermissions|default|plan"`

	NoMCP     bool     `flag:"no-mcp" help:"Disable all MCP servers"`
	NoHooks   bool     `flag:"no-hooks" help:"Skip hooks"`
	NoSkills  bool     `flag:"no-skills" help:"Disable slash commands"`
	SkillDirs []string `flag:"skill-dir" help:"Additional skill/plugin directory (repeatable)"`
	NoUser    bool     `flag:"no-user" help:"Skip user-level settings"`
	NoProject bool     `flag:"no-project" help:"Skip project-level settings"`
	NoMemory  bool     `flag:"no-memory" help:"Skip auto-memory and CLAUDE.md"`
	Bare      bool     `flag:"bare" help:"Skip hooks, skills, memory, and ambient settings"`
}

var validPermissionModes = []string{"acceptEdits", "auto", "bypassPermissions", "default", "plan"}

func validatePermissionMode(s string) error {
	if s == "" {
		return nil
	}
	for _, m := range validPermissionModes {
		if s == m {
			return nil
		}
	}
	return fmt.Errorf("invalid --permission-mode %q (valid: %s)", s, strings.Join(validPermissionModes, "|"))
}

type AIPromptOptions struct {
	AIRuntimeOptions

	// File is a positional .prompt template path rendered through pkg/ai/prompt.
	// The frontmatter sets model + any ai.Request option; the body is the prompt.
	File         string   `args:"true" help:"Path to a .prompt template to render"`
	Prompt       string   `flag:"prompt" help:"Prompt text, or @file to load and render a .prompt template" short:"p"`
	System       string   `flag:"system" help:"System prompt" short:"s"`
	AppendSystem string   `flag:"append-system" help:"Append text to the default system prompt"`
	Var          []string `flag:"var" help:"Template variable key=value (repeatable)" short:"V"`
	Attach       []string `flag:"attach" help:"Attach a local path or URL (repeatable; RFC 4180 comma-separated values allowed)" short:"A"`
	MultiModels  []string `flag:"multi-models" help:"Run prompt once per runtime selector in parallel, e.g. cli:sonnet-5,cmux:opus (repeatable; comma-separated allowed)" short:"M"`
	Timeout      string   `flag:"timeout" help:"Request timeout" default:"120s"`
	NoStream     bool     `flag:"no-stream" help:"Disable streaming; print only the final text to stdout"`
}

type AIPromptResult struct {
	Text        string     `json:"text" pretty:"label=Response"`
	Model       string     `json:"model" pretty:"label=Model"`
	Backend     string     `json:"backend" pretty:"label=Backend"`
	Dir         string     `json:"dir,omitempty" pretty:"label=Dir"`
	SessionID   string     `json:"sessionId,omitempty" pretty:"label=Session"`
	HistoryFile string     `json:"historyFile,omitempty" pretty:"label=History"`
	Input       ai.Request `json:"input" pretty:"-"`
	InputTokens int        `json:"inputTokens" pretty:"label=Input Tokens"`
	Output      int        `json:"outputTokens" pretty:"label=Output Tokens"`
	CostUSD     float64    `json:"costUSD,omitempty" pretty:"label=Cost USD"`
	Duration    string     `json:"duration" pretty:"label=Duration"`
}

// ToRequest translates the runtime knobs into the typed ai.Request, overlaying
// saved defaults from ~/.captain.yaml onto unset fields. Precedence is
// flag > saved > built-in: max-tokens uses the explicit flag when > 0, else the
// saved default, else 4096; --effort uses the flag when set, else saved.
// The ambient toggles are negative flags (--no-mcp, …) that compose with the
// saved No* defaults via OR, so either a flag or a saved default switches a
// feature off; re-enabling a saved-off feature is done via `captain configure`.
//
// systemPrompt / appendSystemPrompt / userPrompt are passed explicitly so
// non-prompt callers (gavel's ai-fix loop) can build them per-iteration without
// leaking those fields into the shared runtime struct. Parse/validation errors
// are returned rather than silently coerced to zero values.
func (o AIRuntimeOptions) ToRequest(systemPrompt, appendSystemPrompt, userPrompt string) (ai.Request, error) {
	saved := loadSavedAI()

	temperature, err := parseFloatFlag("temperature", o.Temperature)
	if err != nil {
		return ai.Request{}, err
	}
	if temperature < 0 || temperature > 2 {
		return ai.Request{}, fmt.Errorf("invalid --temperature %v (valid: 0.0-2.0)", temperature)
	}
	if o.MaxTurns < 0 || o.MaxTurns > 100 {
		return ai.Request{}, fmt.Errorf("invalid --max-turns %d (valid: 0-100, 0=provider default)", o.MaxTurns)
	}
	if err := validatePermissionMode(o.PermissionMode); err != nil {
		return ai.Request{}, err
	}

	maxTokens := o.MaxTokens
	switch {
	case maxTokens > 0: // explicit flag wins
	case saved.MaxTokens != 0:
		maxTokens = saved.MaxTokens
	default:
		maxTokens = 4096
	}

	// Resolve the USD budget onto the request (flag > saved) so backends that read
	// req.Budget.Cost — claude-cli, claude-agent — enforce it without relying on a
	// later config-side reconciliation that not every path performs (finding A4).
	budget, err := parseFloatFlag("budget", o.Budget)
	if err != nil {
		return ai.Request{}, err
	}
	if budget == 0 {
		budget = saved.BudgetUSD
	}

	effort := o.Effort
	if effort == "" {
		effort = saved.ReasoningEffort
	}
	if err := api.Effort(effort).Validate(); err != nil {
		return ai.Request{}, fmt.Errorf("invalid --effort %q: %w", effort, err)
	}

	// Temperature is *float64 on the model: leave it nil for the default 0 so an
	// explicit 0 and "unset" hash identically (matches the prior flat behaviour);
	// no captain provider sends temperature to the model, only the cache key.
	var temperaturePtr *float64
	if temperature != 0 {
		t := temperature
		temperaturePtr = &t
	}

	perms := api.Permissions{
		Mode:  api.PermissionMode(o.PermissionMode),
		Tools: api.Tools{Allow: o.AllowedTools, Deny: o.DisallowedTools},
		MCP:   api.MCP{Disabled: o.NoMCP || saved.NoMCP},
	}
	if o.Edit {
		perms.Presets = append(perms.Presets, api.PresetEdit)
	}

	return ai.Request{
		Prompt: api.Prompt{System: systemPrompt, AppendSystem: appendSystemPrompt, User: userPrompt},
		Model:  api.Model{Temperature: temperaturePtr, Effort: api.Effort(effort), NoCache: o.NoCache || saved.NoCache},
		Budget: api.Budget{Cost: budget, MaxTokens: maxTokens, MaxTurns: o.MaxTurns},
		Memory: api.Memory{
			Skills:      o.SkillDirs,
			SkipHooks:   o.NoHooks || saved.NoHooks,
			SkipSkills:  o.NoSkills || saved.NoSkills,
			SkipUser:    o.NoUser || saved.NoUser,
			SkipProject: o.NoProject || saved.NoProject,
			SkipMemory:  o.NoMemory || saved.NoMemory,
			Bare:        o.Bare,
		},
		Permissions: perms,
		SessionID:   o.Resume,
	}, nil
}

// ToRequest delegates to AIRuntimeOptions.ToRequest, lifting the prompt
// fields the prompt-shaped command owns onto the typed request.
func (o AIPromptOptions) ToRequest() (ai.Request, error) {
	req, err := o.AIRuntimeOptions.ToRequest(o.System, o.AppendSystem, o.Prompt)
	if err != nil {
		return ai.Request{}, err
	}
	req.Prompt.Attachments, err = attachmentRefsFromFlags(o.Attach)
	return req, err
}

// RunAIPrompt is a deprecated alias for `captain prompt run`. It routes through
// the same shared render + execute core (renderPromptSource + executePromptRequest)
// so there is one implementation; the positional `.prompt` file, --prompt/-p, and
// stdin all still work.
func RunAIPrompt(opts AIPromptOptions) (any, error) {
	log.Warnf("`captain ai prompt` is deprecated; use `captain prompt run` (it accepts a .prompt file, id, --prompt/-p, or stdin)")

	var stdin string
	if claude.IsStdinPiped() {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		stdin = string(b)
	}

	ctx := context.Background()
	req, cfg, err := renderPromptSource(ctx, opts.File, opts, "", stdin)
	if err != nil {
		return nil, err
	}
	if req.Prompt.User == "" && len(req.Prompt.Attachments) == 0 {
		return nil, fmt.Errorf("prompt text or attachment required (use --prompt/-p, --attach/-A, a file arg, or pipe via stdin)")
	}
	if cfg.Model.Name == "" {
		return nil, fmt.Errorf("no model: pass --model or run 'captain configure' to set a default")
	}
	if err := req.Validate(); err != nil {
		return nil, err
	}
	if len(opts.MultiModels) > 0 {
		rendered := PromptRenderResult{
			Name:    "prompt",
			Model:   cfg.Model.Name,
			Backend: string(cfg.Model.Backend),
			Input:   req,
			Config:  cfg,
		}
		return executeSyncRun(ctx, rendered, opts)
	}
	return executePromptRequest(ctx, req, cfg, runtimeTimeout(req.Budget.Timeout), opts.NoStream)
}

func executePromptRequest(parent context.Context, req ai.Request, cfg ai.Config, timeout time.Duration, noStream bool) (any, error) {
	ctx, cancel := runContext(parent, req, timeout)
	defer cancel()
	if err := preparePromptAttachments(ctx, &req, cfg); err != nil {
		return nil, err
	}

	p, cleanup, err := buildProvider(ctx, &req, cfg)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if streamer, ok := p.(ai.StreamingProvider); ok && !noStream && !req.Prompt.HasSchema() {
		return runStreaming(ctx, streamer, req)
	}
	return runBuffered(ctx, p, req)
}

// runContext derives the timeout-bounded context for a prompt execution. A
// non-empty req.Budget.Timeout overrides the caller-supplied timeout; a
// non-positive timeout falls back to 120s.
func runContext(parent context.Context, req ai.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if req.Budget.Timeout != "" {
		timeout = runtimeTimeout(req.Budget.Timeout)
	}
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	return context.WithTimeout(parent, timeout)
}

// buildProvider constructs the logging-wrapped AI provider for req/cfg. When
// req.Setup is present it runs the shell preparation (mutating req's cwd/env to
// the prepared sandbox) and returns its cleanup; callers MUST defer the returned
// cleanup. ctx should already carry the run timeout.
// warnIfLikelyModelTypo emits a "did you mean" hint when the model name is not a
// known catalog id but is a close (edit-distance ≤ 2) match to one — catching
// typos like "claud-sonnet-4" even when an explicit backend is configured (so
// InferBackend's suggestion never fires). Non-blocking: an unrecognized model may
// still be a valid provider/OpenRouter id, so the run proceeds.
func warnIfLikelyModelTypo(model string) {
	if suggestion, ok := suggestKnownModel(model); ok {
		log.Warnf("model %q is not a recognized model; did you mean %q?", model, suggestion)
	}
}

// suggestKnownModel returns the closest known model to model and true when it is
// unrecognized-but-close (edit-distance ≤ 2) — a likely typo. A model known to
// the catalog or the pricing registry, or one far from any known name (a
// plausibly-valid id we simply don't list), returns ("", false).
//
// The catalog is checked first (in-memory, no I/O); only a catalog miss consults
// the pricing registry, which loads from its disk cache (fetching once if stale)
// and degrades to catalog-only if unavailable.
func suggestKnownModel(model string) (string, bool) {
	if model == "" {
		return "", false
	}
	for _, m := range ai.Catalog() {
		if m.ID == model || baseModelName(m.ID) == model {
			return "", false // known catalog model
		}
	}
	if pricing.Contains(model) {
		return "", false // known pricing-registry (e.g. OpenRouter) model
	}
	return closestModel(model, knownModelNames())
}

// knownModelNames is every model name captain can suggest: catalog canonical ids
// and their base names, plus the pricing registry's ids.
func knownModelNames() []string {
	catalog := ai.Catalog()
	names := make([]string, 0, len(catalog)*2+pricing.RegistrySize())
	for _, m := range catalog {
		names = append(names, m.ID, baseModelName(m.ID))
	}
	for _, mi := range pricing.ListModels("") {
		names = append(names, mi.ModelID, baseModelName(mi.ModelID))
	}
	return names
}

// closestModel returns the nearest candidate to model and true when it is a
// likely typo (edit-distance ≤ 2, not an exact match).
func closestModel(model string, candidates []string) (string, bool) {
	lower := strings.ToLower(model)
	best, bestDist := "", -1
	for _, cand := range candidates {
		if cand == model {
			return "", false
		}
		if d := collections.Levenshtein(lower, strings.ToLower(cand)); bestDist < 0 || d < bestDist {
			best, bestDist = cand, d
		}
	}
	if best != "" && bestDist >= 0 && bestDist <= 2 {
		return best, true
	}
	return "", false
}

func baseModelName(id string) string {
	if i := strings.LastIndex(id, "/"); i >= 0 {
		return id[i+1:]
	}
	return id
}

func buildProvider(ctx context.Context, req *ai.Request, cfg ai.Config) (ai.Provider, func(), error) {
	for _, c := range cfg.Model.Candidates() {
		warnIfLikelyModelTypo(c.Name)
	}
	cleanup := func() {}
	if req.NoCache {
		cfg.NoCache = true
	}
	if req.Setup != nil {
		setup, err := shell.Prepare(dbcontext.NewContext(ctx), req.Setup)
		if err != nil {
			return nil, cleanup, err
		}
		cleanup = func() { _ = setup.Cleanup() }
		req.SetCwd(setup.Cwd)
		req.Setup.Env = setup.Env
	}
	p, err := ai.NewProvider(cfg)
	if err != nil {
		cleanup()
		return nil, func() {}, err
	}
	if p, err = middleware.Wrap(p, middleware.WithLogging(), middleware.WithSchemaValidation(cfg)); err != nil {
		cleanup()
		return nil, func() {}, err
	}
	return p, cleanup, nil
}

func runBuffered(ctx context.Context, p ai.Provider, req ai.Request) (any, error) {
	start := time.Now()
	resp, err := p.Execute(ctx, req)
	if err != nil {
		return nil, err
	}
	model := firstNonEmpty(resp.Model, p.GetModel(), req.Name)
	backend := firstNonEmpty(string(resp.Backend), string(p.GetBackend()), string(req.Backend))
	input := resolvedPromptInput(req, model, backend, req.SessionID)
	dir := actualRunDir(input)
	return AIPromptResult{
		Text:        resp.Text,
		Model:       model,
		Backend:     backend,
		Dir:         dir,
		SessionID:   input.SessionID,
		HistoryFile: historyFileForRun(api.Backend(backend), input.SessionID, dir),
		Input:       input,
		InputTokens: resp.Usage.InputTokens,
		Output:      resp.Usage.OutputTokens,
		Duration:    time.Since(start).Round(time.Millisecond).String(),
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
		session = req.SessionID
	)
	renderer := newLineRenderer(os.Stderr, 8)
	loop, err := ai.RunUntil(ctx, ai.LoopOptions{
		Provider:      sp,
		MaxIterations: 1,
		MaxCostUSD:    req.Budget.Cost, // enforce the USD budget
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
			if ev.SessionID != "" {
				session = ev.SessionID
			}
			renderEvent(os.Stderr, renderer, ev)
			if ev.Kind == ai.EventText {
				text += ev.Text
			}
			if ev.Kind == ai.EventResult {
				if len(ev.StructuredData) > 0 {
					text = string(ev.StructuredData)
				}
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
	if loop.StopReason == "max-cost" {
		return nil, fmt.Errorf("%w: spent $%.4f of $%.4f budget", ai.ErrBudgetExceeded, loop.TotalCost, req.Budget.Cost)
	}
	if session == "" && len(loop.Iterations) > 0 {
		session = loop.Iterations[0].SessionID
	}
	input := resolvedPromptInput(req, model, backend, session)
	dir := actualRunDir(input)
	return AIPromptResult{
		Text:        text,
		Model:       model,
		Backend:     backend,
		Dir:         dir,
		SessionID:   input.SessionID,
		HistoryFile: historyFileForRun(api.Backend(backend), input.SessionID, dir),
		Input:       input,
		InputTokens: usage.InputTokens,
		Output:      usage.OutputTokens,
		CostUSD:     cost,
		Duration:    time.Since(start).Round(time.Millisecond).String(),
	}, nil
}

func resolvedPromptInput(req ai.Request, model, backend, sessionID string) ai.Request {
	out := req
	if model != "" {
		out.Name = model
	}
	if backend != "" {
		out.Backend = api.Backend(backend)
	}
	if sessionID != "" {
		out.SessionID = sessionID
	}
	return out
}

func actualRunDir(req ai.Request) string {
	if cwd := req.Cwd(); cwd != "" {
		return cwd
	}
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return wd
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
	if tu, ok := ev.Raw.(claude.ToolUse); ok {
		if renderCodexEntry(renderer, ev, tu) {
			return
		}
	}

	switch ev.Kind {
	case ai.EventText:
		fmt.Fprintf(w, "%s", ev.Text)
	case ai.EventThinking:
		if log.IsDebugEnabled() {
			fmt.Fprintf(w, "[thinking] %s\n", truncForStderr(ev.Text, 200))
		}
	case ai.EventToolUse:
		fmt.Fprintf(w, "\n[tool] %s %s\n", ev.Tool, summariseInput(ev.Input))
	case ai.EventPermission:
		fmt.Fprintf(w, "\n[permission] %s %s awaiting approval\n", ev.Tool, summariseInput(ev.Input))
	case ai.EventToolResult:
		if ev.Text != "" {
			label := "tool-result"
			if !ev.Success {
				label = "tool-error"
			}
			fmt.Fprintf(w, "[%s] %s\n", label, truncForStderr(ev.Text, 500))
		}
	case ai.EventResult:
		renderResultEvent(renderer, ev)
	case ai.EventError:
		fmt.Fprintf(w, "\n[error] %s\n", ev.Error)
		log.Errorf("%s", ev.Error)
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

// renderCodexEntry mirrors renderClaudeEntry for codex live events, which
// stash a synthesized claude.ToolUse on ev.Raw rather than a HistoryEntry
// (codex's stream schema does not match Claude's message-shaped envelope).
// Routing the codex tool use through ToolUsesToTools keeps the rendering
// path identical to `captain history` for codex JSONL.
func renderCodexEntry(renderer *lineRenderer, ev ai.Event, tu claude.ToolUse) bool {
	switch ev.Kind {
	case ai.EventToolUse, ai.EventResult, ai.EventSystem:
	default:
		return false
	}
	tl := claude.ToolUsesToTools([]claude.ToolUse{tu})
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
	cfg, err := opts.ToConfig()
	if err != nil {
		return nil, err
	}
	if cfg.Model.Name == "" {
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
		Prompt: api.Prompt{User: "Respond with exactly: ok"},
		Budget: api.Budget{MaxTokens: 10},
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
