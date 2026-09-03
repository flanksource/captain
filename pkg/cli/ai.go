package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/ai/agent/setup"
	"github.com/flanksource/captain/pkg/ai/middleware"
	"github.com/flanksource/captain/pkg/ai/pricing"
	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/api/registry"
	"github.com/flanksource/captain/pkg/captainconfig"
	"github.com/flanksource/captain/pkg/collections"
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

// AIProviderOptions binds model selection plus the knobs that belong to the
// request rather than the model: the endpoint, the API key and the spend budget.
//
// The model flags themselves live in pkg/aiflags — a leaf any clicky CLI can embed
// without inheriting pkg/cli's ~1000 transitive packages. Embedding it here keeps
// captain's flag surface unchanged (clicky promotes embedded flags at any depth)
// while giving downstream repos the same parsing captain uses.
type AIProviderOptions struct {
	aiflags.ModelFlags

	APIKey  string `flag:"api-key" help:"API key (env: ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, GOOGLE_API_KEY, DEEPSEEK_API_KEY)"`
	APIURL  string `flag:"api-url" help:"Override the provider endpoint, e.g. a 'captain ai mock' URL. Required for the openai cli mode, which ignores OPENAI_BASE_URL when a ChatGPT credential is stored"`
	Budget  string `flag:"budget" help:"Max spend in USD, 0=unlimited" default:"0"`
	Sandbox string `flag:"sandbox" help:"Sandbox for the run: off|native|docker|git-agent, or a configured Docker/Git Agent backend"`
}

// SandboxSelector trims the public sandbox mode or configured backend selector.
func (o AIProviderOptions) SandboxSelector() string {
	return strings.TrimSpace(o.Sandbox)
}

// BudgetUSD parses --budget, failing loud on malformed input.
func (o AIProviderOptions) BudgetUSD() (float64, error) {
	return parseFloatFlag("budget", o.Budget)
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
	budget, err := o.BudgetUSD()
	if err != nil {
		return ai.Config{}, err
	}
	if budget == 0 {
		budget = saved.BudgetUSD
	}

	// Sandbox precedence here is flag > global default > none: this path has no
	// prompt file, so there is no frontmatter layer (that one is overlayCLI's).
	sandbox, err := resolveSandboxSelection(o.SandboxSelector(), nil, savedCfg.Sandbox)
	if err != nil {
		return ai.Config{}, err
	}

	// One resolve: flags → Model → saved per-provider defaults → catalog. The
	// warn-and-continue policy for a broken config stays here (loadSavedConfig), so
	// aiflags can hand the error back instead of swallowing it.
	flags := o.ModelFlags
	if forced := sandboxForcedMode(sandbox.Kind); forced != "" {
		if value := strings.TrimSpace(flags.Mode); value != "" {
			mode, ok := registry.ParseRuntimeMode(value)
			if !ok {
				return ai.Config{}, fmt.Errorf("invalid --mode %q (valid: %s)", value, registry.RuntimeModeList())
			}
			if mode != forced {
				return ai.Config{}, fmt.Errorf("sandbox %q requires %s mode, but --mode is %q", sandbox.Kind, forced, mode)
			}
		}
		flags.Mode = string(forced)
	}
	m, err := flags.ResolveWith(saved)
	if err != nil {
		return ai.Config{}, err
	}
	return ai.Config{
		Model:            m,
		Budget:           api.Budget{Cost: budget},
		APIKey:           o.APIKey,
		APIURL:           strings.TrimSpace(o.APIURL),
		SandboxSelection: sandboxSelectionConfig(sandbox, nil),
		NoCache:          o.NoCache || saved.NoCache,
		SchemaRepair:     schemaRepairConfig(savedCfg.Prompts.SchemaRepair),
	}, nil
}

// schemaRepairConfig reads the `model:`/`mode:` pair out of ~/.captain.yaml.
// `mode:` names the mechanism (api|agent|cli|cmux); the provider follows from the
// model name, and the pair is resolved where the repair provider is built.
func schemaRepairConfig(saved captainconfig.SchemaRepairDefaults) api.SchemaRepairConfig {
	return api.SchemaRepairConfig{
		Model: api.Model{
			Name: strings.TrimSpace(saved.Model),
			Mode: api.RuntimeMode(strings.TrimSpace(saved.Mode)),
		},
		Prompt: strings.TrimSpace(saved.Prompt),
	}
}

func isZeroSchemaRepair(c api.SchemaRepairConfig) bool {
	return strings.TrimSpace(c.Prompt) == "" &&
		c.Model.Name == "" &&
		c.Model.ID == "" &&
		c.Model.Mode == "" &&
		c.Model.Provider == nil &&
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

	// Effort and Temperature are NOT here: they describe the model and so live on
	// the embedded aiflags.ModelFlags, promoted through AIProviderOptions.
	// Redeclaring them would bind --effort twice and panic cobra at init.
	MaxTokens int    `flag:"max-tokens" help:"Maximum output tokens (0 = saved default or 4096)"`
	MaxTurns  int    `flag:"max-turns" help:"Max agent turns 0-100, 0 = provider default (agent mode)"`
	Resume    string `flag:"resume" help:"Resume an existing session by id (agent and cli modes)"`

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
	Prompt       string   `flag:"prompt" clicky:"cli-file-read" help:"Prompt text, or @file to load and render a .prompt template" short:"p"`
	System       string   `flag:"system" help:"System prompt" short:"s"`
	AppendSystem string   `flag:"append-system" help:"Append text to the default system prompt"`
	Var          []string `flag:"var" help:"Template variable key=value (repeatable)" short:"V"`
	Attach       []string `flag:"attach" help:"Attach a local path or URL (repeatable; RFC 4180 comma-separated values allowed)" short:"A"`
	MultiModels  []string `flag:"multi-models" help:"Run prompt once per runtime selector in parallel, e.g. cli:sonnet-5,cmux:opus (repeatable; comma-separated allowed)" short:"M"`
	Timeout      string   `flag:"timeout" help:"Request timeout (default 120s; a relocating sandbox waits for the remote agent instead)"`
	NoStream     bool     `flag:"no-stream" help:"Disable streaming; print only the final text to stdout"`

	// RuntimeProfile is the catalog profile (id or name) `captain prompt
	// run|render --runtime-profile` layers beneath the frontmatter. It is not a
	// flag here: the deprecated `captain ai prompt` alias does not grow it.
	RuntimeProfile string
}

type AIPromptResult struct {
	Text             string         `json:"text" pretty:"label=Response"`
	StructuredOutput map[string]any `json:"structuredOutput,omitempty" pretty:"-"`
	Model            string         `json:"model" pretty:"label=Model"`
	Provider         string         `json:"provider" pretty:"label=Provider"`
	Mode             string         `json:"mode" pretty:"label=Mode"`
	Dir              string         `json:"dir,omitempty" pretty:"label=Dir"`
	SessionID        string         `json:"sessionId,omitempty" pretty:"label=Session"`
	HistoryFile      string         `json:"historyFile,omitempty" pretty:"label=History"`
	Input            ai.Request     `json:"input" pretty:"-"`
	InputTokens      int            `json:"inputTokens" pretty:"label=Input Tokens"`
	Output           int            `json:"outputTokens" pretty:"label=Output Tokens"`
	CostUSD          float64        `json:"costUSD,omitempty" pretty:"label=Cost USD"`
	Duration         string         `json:"duration" pretty:"label=Duration"`
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

	// Resolve the USD budget onto the request (flag > saved) so the runtimes that
	// read req.Budget.Cost — the anthropic cli and agent — enforce it without a
	// later config-side reconciliation that not every path performs (finding A4).
	budget, err := parseFloatFlag("budget", o.Budget)
	if err != nil {
		return ai.Request{}, err
	}
	if budget == 0 {
		budget = saved.BudgetUSD
	}

	effort := o.Effort
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
		Tools: api.ToolsFromLists(o.AllowedTools, o.DisallowedTools),
		MCP:   api.MCP{Disabled: o.NoMCP || saved.NoMCP},
	}
	perms.Mode = api.PermissionMode(o.PermissionMode)
	if o.Edit {
		perms.Presets = append(perms.Presets, api.PresetEdit)
		if perms.Mode == "" {
			perms.Mode = api.PermissionAcceptEdits
		}
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

func executePromptRequest(parent context.Context, req ai.Request, cfg ai.Config, timeout time.Duration, noStream bool) (any, error) {
	ctx, cancel, err := runContext(parent, req, remoteAwareTimeout(req, cfg, timeout))
	if err != nil {
		return nil, err
	}
	defer cancel()
	if err := preparePromptAttachments(ctx, &req, cfg); err != nil {
		return nil, err
	}

	p, cleanup, err := buildProvider(ctx, &req, cfg)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	defer closeProvider(p)

	if streamer, ok := p.(ai.StreamingProvider); ok && !noStream && !req.Prompt.HasSchema() {
		return runStreaming(ctx, streamer, req)
	}
	return runBuffered(ctx, p, req)
}

// runContext derives the timeout-bounded context for a prompt execution. A
// parseable req.Budget.Timeout overrides the caller-supplied timeout; an
// unparseable one is an error, not a silent substitution. A caller-supplied
// timeout of zero falls back to the CLI default.
func runContext(parent context.Context, req ai.Request, timeout time.Duration) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		parent = context.Background()
	}
	declared, err := runtimeTimeout(req.Budget.Timeout)
	if err != nil {
		return nil, nil, err
	}
	if declared > 0 {
		timeout = declared
	}
	if timeout <= 0 {
		timeout = defaultRunTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return ctx, cancel, nil
}

// warnIfLikelyModelTypo emits a "did you mean" hint when the model name is not a
// known catalog id but is a close (edit-distance ≤ 2) match to one — catching
// typos like "claud-sonnet-4" even when an explicit mode is configured (so
// ProviderFor's suggestion never fires). Non-blocking: an unrecognized model may
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

// buildProvider constructs the logging-wrapped AI provider for req/cfg, applying
// req.Setup first so req describes the prepared workspace rather than asking for
// one (see pkg/ai/agent/setup). Callers MUST defer the returned cleanup; ctx
// should already carry the run timeout.
func buildProvider(ctx context.Context, req *ai.Request, cfg ai.Config) (ai.Provider, func(), error) {
	for _, c := range cfg.Model.Candidates() {
		warnIfLikelyModelTypo(c.Name)
	}
	cleanup := func() {}
	if req.NoCache {
		cfg.NoCache = true
	}
	// A remote-executing sandbox replaces provider execution wholesale: the
	// run happens on another machine, so no local setup checkout, no CLI
	// process, no streaming. Resolved before setup.Apply because the sandbox
	// is itself a workspace isolator — a local checkout would double-isolate.
	if remote, err := remoteExecProviderFor(req, cfg); err != nil {
		return nil, cleanup, err
	} else if remote != nil {
		wrapped, err := middleware.Wrap(remote, middleware.WithLogging(), middleware.WithSchemaValidation(cfg))
		if err != nil {
			closeProvider(remote)
			return nil, cleanup, err
		}
		return bufferedOnlyProvider{Provider: wrapped}, cleanup, nil
	}
	prepared, err := setup.Apply(ctx, req, "")
	if err != nil {
		return nil, cleanup, err
	}
	if prepared != nil && prepared.Cleanup != nil {
		cleanup = func() { _ = prepared.Cleanup() }
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
	runtime := firstRuntime(resp.Runtime, p.GetRuntime(), api.RuntimeOf(req.Provider, req.Mode))
	input := resolvedPromptInput(req, model, runtime, req.SessionID)
	dir := actualRunDir(input)
	structuredOutput, err := structuredOutputMap(resp.StructuredData)
	if err != nil {
		return nil, err
	}
	text, err := structuredOutputText(resp.Text, structuredOutput)
	if err != nil {
		return nil, err
	}
	return AIPromptResult{
		Text:             text,
		StructuredOutput: structuredOutput,
		Model:            model,
		Provider:         runtime.Provider,
		Mode:             string(runtime.Mode),
		Dir:              dir,
		SessionID:        input.SessionID,
		HistoryFile:      historyFileForRun(providerOf(runtime), runtime.Mode, input.SessionID, dir),
		Input:            input,
		InputTokens:      resp.Usage.InputTokens,
		Output:           resp.Usage.OutputTokens,
		Duration:         time.Since(start).Round(time.Millisecond).String(),
	}, nil
}

// runStreaming drives the streaming provider through ai.RunUntil with a single
// iteration, rendering live tool/text events to stderr while accumulating the
// final text/usage/cost into AIPromptResult on stdout.
func runStreaming(ctx context.Context, sp ai.StreamingProvider, req ai.Request) (any, error) {
	start := time.Now()
	var (
		text             string
		usage            ai.Usage
		cost             float64
		runtime          = sp.GetRuntime()
		model            = sp.GetModel()
		session          = req.SessionID
		structuredOutput map[string]any
		structuredErr    error
	)
	renderer := NewEventRenderer(os.Stderr)
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
		OnEvent: func(iteration int, ev ai.Event) {
			if ev.Model != "" {
				model = ev.Model
			}
			if ev.SessionID != "" {
				session = ev.SessionID
			}
			renderer.Handle(iteration, ev)
			if ev.Kind == ai.EventText {
				text += ev.Text
			}
			if ev.Kind == ai.EventResult {
				if len(ev.StructuredData) > 0 {
					text = string(ev.StructuredData)
					structuredOutput, structuredErr = structuredOutputMap(ev.StructuredData)
				}
				if ev.Usage != nil {
					usage = *ev.Usage
				}
				cost = ev.CostUSD
			}
		},
	})
	if renderErr := renderer.Flush(); renderErr != nil {
		return nil, errors.Join(err, renderErr)
	}
	if err != nil {
		return nil, err
	}
	if structuredErr != nil {
		return nil, structuredErr
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
	input := resolvedPromptInput(req, model, runtime, session)
	dir := actualRunDir(input)
	return AIPromptResult{
		Text:             text,
		StructuredOutput: structuredOutput,
		Model:            model,
		Provider:         runtime.Provider,
		Mode:             string(runtime.Mode),
		Dir:              dir,
		SessionID:        input.SessionID,
		HistoryFile:      historyFileForRun(providerOf(runtime), runtime.Mode, input.SessionID, dir),
		Input:            input,
		InputTokens:      usage.InputTokens,
		Output:           usage.OutputTokens,
		CostUSD:          cost,
		Duration:         time.Since(start).Round(time.Millisecond).String(),
	}, nil
}

// firstRuntime is the first fully-resolved (provider, mode) pair among the
// candidates: what the response reported, else what the provider serves, else
// what the request asked for.
func firstRuntime(candidates ...api.Runtime) api.Runtime {
	for _, candidate := range candidates {
		if candidate.Valid() {
			return candidate
		}
	}
	return api.Runtime{}
}

func providerOf(runtime api.Runtime) *api.ModelProvider {
	p, _ := runtime.ModelProvider()
	return p
}

func resolvedPromptInput(req ai.Request, model string, runtime api.Runtime, sessionID string) ai.Request {
	out := req
	if model != "" {
		out.Name = model
	}
	if runtime.Valid() {
		out.Provider = providerOf(runtime)
		out.Mode = runtime.Mode
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

type AITestOptions struct {
	AIProviderOptions
	Timeout string `flag:"timeout" help:"Request timeout" default:"60s"`
}

type AITestResult struct {
	Model    string `json:"model" pretty:"label=Model"`
	Provider string `json:"provider" pretty:"label=Provider"`
	Mode     string `json:"mode" pretty:"label=Mode"`
	Status   string `json:"status" pretty:"label=Status"`
	Latency  string `json:"latency" pretty:"label=Latency"`
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
		Model:    p.GetModel(),
		Provider: p.GetRuntime().Provider,
		Mode:     string(p.GetRuntime().Mode),
		Latency:  time.Since(start).Round(time.Millisecond).String(),
	}

	if err != nil {
		result.Status = fmt.Sprintf("FAIL: %v", err)
	} else {
		result.Status = "OK"
	}
	return result, nil
}
