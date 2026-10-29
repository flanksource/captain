package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/flanksource/captain/pkg/ai"
	"github.com/flanksource/captain/pkg/aiflags"
	"github.com/flanksource/captain/pkg/api"
	"github.com/flanksource/captain/pkg/captainconfig"
)

func loadSavedConfig() (captainconfig.Config, error) {
	cfg, _, err := captainconfig.Load()
	if err != nil {
		return captainconfig.Config{}, fmt.Errorf("load Captain configuration: %w", err)
	}
	return cfg, nil
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

	APIKey  string `flag:"api-key" help:"API key (env: ANTHROPIC_API_KEY, OPENAI_API_KEY, GEMINI_API_KEY, GOOGLE_API_KEY, DEEPSEEK_API_KEY)" category:"Model runtime"`
	APIURL  string `flag:"api-url" help:"Override the provider endpoint, e.g. a 'captain ai mock' URL. Required for the openai cli mode, which ignores OPENAI_BASE_URL when a ChatGPT credential is stored" category:"Model runtime"`
	Budget  string `flag:"budget" help:"Max spend in USD, 0=unlimited" category:"Model runtime"`
	Sandbox string `flag:"sandbox" help:"Sandbox for the run: off|native|docker|git-agent, or a configured Docker/Git Agent backend" category:"Model permissions"`
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
	ExplicitFields api.FieldPresence `flag:"-" json:"-" yaml:"-"`

	// Effort and Temperature are NOT here: they describe the model and so live on
	// the embedded aiflags.ModelFlags, promoted through AIProviderOptions.
	// Redeclaring them would bind --effort twice and panic cobra at init.
	MaxTokens int    `flag:"max-tokens" help:"Maximum output tokens (unset = saved default, else the runtime's own default; 0 = runtime default)" category:"Model runtime"`
	MaxTurns  int    `flag:"max-turns" help:"Max agent turns 0-100, 0 = provider default (agent mode)" category:"Model runtime"`
	Resume    string `flag:"resume" help:"Resume an existing session by id (agent and cli modes)" category:"Model runtime"`

	Edit            bool     `flag:"edit" help:"Safe defaults: acceptEdits + Read/Edit/Write/Glob/Grep allowlist" category:"Model permissions"`
	AllowedTools    []string `flag:"allowed-tools" help:"Override --edit's built-in allowlist (claude only)" category:"Model permissions"`
	DisallowedTools []string `flag:"disallowed-tools" help:"Tools to deny (claude only)" category:"Model permissions"`
	PermissionMode  string   `flag:"permission-mode" help:"acceptEdits|auto|bypassPermissions|default|plan" category:"Model permissions"`
	Access          string   `flag:"access" help:"Portable access contract compiled per runtime: read-only|full" category:"Model permissions"`
	// Perms is nil when --perms was not passed, which selects the host's
	// AIRuntimeResolveOptions.DefaultPerms; a non-nil selection whose entries
	// are all blank (`--perms ''`) selects none.
	Perms []string `flag:"perms" help:"Permission sets (runtime preset ids or names) layered beneath the CLI flags; repeatable. Empty string selects none" category:"Model permissions"`

	NoMCP     bool     `flag:"no-mcp" help:"Disable all MCP servers" category:"Model permissions"`
	NoHooks   bool     `flag:"no-hooks" help:"Skip hooks" category:"Model runtime"`
	NoSkills  bool     `flag:"no-skills" help:"Disable slash commands" category:"Model runtime"`
	SkillDirs []string `flag:"skill-dir" help:"Additional skill/plugin directory (repeatable)" category:"Model runtime"`
	NoUser    bool     `flag:"no-user" help:"Skip user-level settings" category:"Model runtime"`
	NoProject bool     `flag:"no-project" help:"Skip project-level settings" category:"Model runtime"`
	NoMemory  bool     `flag:"no-memory" help:"Skip auto-memory and CLAUDE.md" category:"Model runtime"`
	Bare      bool     `flag:"bare" help:"Skip hooks, skills, memory, and ambient settings" category:"Model runtime"`
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

	File            string   `args:"true" help:"Prompt name, ID, .prompt path, or - for stdin"`
	Prompt          string   `flag:"prompt" clicky:"cli-file-read" help:"Prompt text, or @file to load and render a .prompt template" short:"p"`
	System          string   `flag:"system" clicky:"cli-file-read" help:"System prompt" short:"s"`
	AppendSystem    string   `flag:"append-system" clicky:"cli-file-read" help:"Append text to the default system prompt"`
	Var             []string `flag:"var" split:"false" help:"Template variable (repeatable): key=value (@file loads it, - reads stdin), key:=json, key[]=@file or key[]=- for a list of lines" short:"V"`
	Attach          []string `flag:"attach" help:"Attach a local path or URL (repeatable; RFC 4180 comma-separated values allowed)" short:"A"`
	MultiModels     []string `flag:"multi-models" help:"Run prompt once per runtime selector in parallel, e.g. cli:sonnet-5,cmux:opus (repeatable; comma-separated allowed)" short:"M" category:"Model selection"`
	Timeout         string   `flag:"timeout" help:"Request timeout (default 120s; a relocating sandbox waits for the remote agent instead)" category:"Model runtime"`
	NoStream        bool     `flag:"no-stream" help:"Disable streaming; print only the final text to stdout" category:"Model runtime"`
	Presets         []string `flag:"preset" help:"Runtime preset layered into the prompt (repeatable)" category:"Model runtime"`
	NoPresets       bool     `flag:"no-presets" help:"Clear presets selected by prompt frontmatter" category:"Model runtime"`
	RequiredPresets []string `flag:"-" json:"-" yaml:"-"`
	PresetsSet      bool

	// RuntimeProfile is the catalog profile (id or name) `captain prompt
	// run|render --runtime-profile` used to layer beneath the frontmatter. It is
	// retained only to warn and no-op.
	RuntimeProfile string `flag:"runtime-profile" help:"Deprecated and ignored; use --preset" category:"Model runtime"`

	Vars string `flag:"vars" clicky:"cli-file-read" help:"JSON object of template variables (or @file.json)"`

	// PromptRef is the raw --prompt/-p value as typed, before an @reference was
	// expanded into the file's contents. The expansion is what makes `-p @file`
	// work, but it also erases the path — and the path is what says whether the
	// caller pointed at an authored .prompt template or at a document.
	PromptRef string

	// InputFiles are the positional input files after the prompt; each run binds
	// them as the `files` var, and as `file` when there is exactly one.
	InputFiles []string `flag:"-" json:"-" yaml:"-"`
	// Batch and BatchConcurrency split the input files into runs of Batch files,
	// BatchConcurrency at a time (spec field batch).
	Batch            int `flag:"batch" help:"Split the input files into runs of N files each (spec batch.size)" category:"Model runtime"`
	BatchConcurrency int `flag:"batch-concurrency" help:"How many batched runs execute at once (spec batch.concurrency, default 1)" category:"Model runtime"`

	// deferInputHooks renders the frontmatter without running its input hooks,
	// for a render that only reads how to split the input files.
	deferInputHooks bool
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

	TerminalOutcome *ai.TerminalOutcome `json:"-" pretty:"-"`
}
